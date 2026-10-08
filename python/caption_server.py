#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = [
#   "fastapi>=0.115",
#   "uvicorn>=0.30",
#   "transformers==4.49.0",
#   "torch>=2.2,<3",
#   "einops>=0.8",
#   "timm>=1.0",
#   "accelerate>=1.0",
#   "pillow>=10.0",
# ]
# ///
"""Local Florence-2 captioning sidecar for kvm-cli.

Receives base64-encoded image crops and returns captions generated with the
pinned Florence-2 model using OmniParser's <MORE_DETAILED_CAPTION> task. Managed
by kvm-cli (`kvm-cli cua captioner serve`); spoken to over plain HTTP.

Device autodetect: CUDA (NVIDIA) -> MPS (Apple Silicon / Metal) -> CPU.
Override with --device cuda|mps|cpu.
"""

from __future__ import annotations

import argparse
import base64
import faulthandler
import io
import json
import os
import signal
import sys
import threading
import time

# Ops hook: kill -USR1 <pid> dumps all thread stacks (stuck-load diagnostics).
if os.environ.get("CAPTIONER_FAULTHANDLER"):
    faulthandler.register(signal.SIGUSR1, chain=False)

from fastapi import FastAPI
from PIL import Image
from pydantic import BaseModel

MODEL_ID = "microsoft/Florence-2-base"
TASK_PROMPT = "<MORE_DETAILED_CAPTION>"  # OmniParser-v2's captioning task

_state = {
    "model": None,
    "processor": None,
    "device": None,
    "ready": False,
    "error": None,
    "load_started": None,
    "load_seconds": None,
    "lock": threading.Lock(),
}

app = FastAPI(title="kvm-cli captioner", version="1.0.0")


class CaptionRequest(BaseModel):
    images: list[str]  # base64 PNG/JPEG crops
    task: str = TASK_PROMPT
    max_new_tokens: int = 64


def _pick_device(explicit: str | None) -> str:
    if explicit:
        return explicit
    try:
        import torch

        if torch.cuda.is_available():
            return "cuda"
        if torch.backends.mps.is_available():
            return "mps"
    except Exception:
        pass
    return "cpu"


def _load(model_id: str, device: str | None) -> None:
    import torch
    from transformers import AutoModelForCausalLM, AutoProcessor

    picked = _pick_device(device)
    started = time.time()
    sys.stderr.write(f"[captioner] loading {model_id} on {picked} ...\n")
    sys.stderr.flush()

    processor = AutoProcessor.from_pretrained(model_id, trust_remote_code=True)
    dtype = torch.float16 if picked in ("cuda", "mps") else torch.float32
    model = AutoModelForCausalLM.from_pretrained(
        model_id,
        trust_remote_code=True,
        torch_dtype=dtype,
    ).to(picked)
    model.eval()

    _state["model"] = model
    _state["processor"] = processor
    _state["device"] = picked
    _state["load_seconds"] = time.time() - started
    _state["ready"] = True
    sys.stderr.write(
        f"[captioner] ready on {picked} in {_state['load_seconds']:.1f}s\n"
    )
    sys.stderr.flush()


def _decode_crop(b64: str) -> Image.Image:
    raw = base64.b64decode(b64)
    img = Image.open(io.BytesIO(raw))
    return img.convert("RGB")


@app.get("/health")
def health() -> dict:
    return {
        "ready": _state["ready"],
        "device": _state["device"],
        "model": MODEL_ID,
        "task": TASK_PROMPT,
        "error": _state["error"],
        "load_started": _state["load_started"],
        "load_seconds": _state["load_seconds"],
    }


@app.post("/caption")
def caption(req: CaptionRequest) -> dict:
    if not _state["ready"]:
        return {"captions": [], "error": _state["error"] or "model not loaded yet"}
    if not req.images:
        return {"captions": [], "error": "no images"}
    import torch

    crops = [_decode_crop(b64) for b64 in req.images]
    with _state["lock"]:
        # All crops share the task prompt, so the batch shares one token layout.
        inputs = _state["processor"](
            text=[req.task] * len(crops), images=crops, return_tensors="pt"
        ).to(_state["device"])
        # Half-precision models (cuda/mps) need float16 pixel values; token ids
        # stay long.
        param_dtype = next(_state["model"].parameters()).dtype
        if "pixel_values" in inputs and inputs["pixel_values"].dtype != param_dtype:
            inputs["pixel_values"] = inputs["pixel_values"].to(param_dtype)
        with torch.no_grad():
            generated = _state["model"].generate(
                input_ids=inputs["input_ids"],
                pixel_values=inputs["pixel_values"],
                max_new_tokens=req.max_new_tokens,
                num_beams=1,
                do_sample=False,
            )
        texts = _state["processor"].batch_decode(generated, skip_special_tokens=True)
    return {"captions": [t.strip() for t in texts], "error": None}


def main() -> None:
    ap = argparse.ArgumentParser(description="kvm-cli local captioner")
    ap.add_argument("--port", type=int, default=8618)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--model", default=MODEL_ID)
    ap.add_argument("--device", default=None, help="cuda | mps | cpu (default: autodetect)")
    ap.add_argument("--warmup", action="store_true", help="load the model at startup")
    args = ap.parse_args()

    _state["load_started"] = time.time()

    def warm() -> None:
        try:
            _load(args.model, args.device)
        except Exception as exc:  # noqa: BLE001
            _state["error"] = f"{type(exc).__name__}: {exc}"
            sys.stderr.write(f"[captioner] load failed: {_state['error']}\n")
            sys.stderr.flush()

    if args.warmup:
        threading.Thread(target=warm, daemon=True).start()

    import uvicorn

    uvicorn.run(app, host=args.host, port=args.port, log_level="warning")


if __name__ == "__main__":
    main()
