package grounding

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"sync"
	"time"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"

	"github.com/roboalchemist/kvm-cli/pkg/yolo"
)

// ONNX tensor names used by the ultralytics export of icon_detect.
const (
	InputName  = "images"
	OutputName = "output0"
	// TensorSize is the letterboxed input edge (YOLOv8 default inference size).
	TensorSize = 640
)

// LocalProvider grounds images in-process by running the icon_detect YOLO model
// through ONNX Runtime. It returns interactive icon boxes only — there is no
// OCR captioning (that is the Florence-2 half of OmniParser), so Content is
// empty on every element and the caller is expected to read the screenshot.
type LocalProvider struct {
	// ModelPath is the ONNX model file (pinned icon_detect export).
	ModelPath string
	// LibPath is the ONNX Runtime shared library. Empty searches system paths.
	LibPath string
	// EPs are the execution providers in preference order. Empty uses the
	// runtime default.
	EPs []string

	once    sync.Once
	rt      *ort.Runtime
	env     *ort.Env
	session *ort.Session
	initErr error

	// runInference, when set (tests), replaces the ONNX Runtime session path.
	runInference func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error)
}

// init lazily boots ONNX Runtime exactly once; errors are sticky.
func (p *LocalProvider) init() error {
	p.once.Do(func() {
		rt, err := ort.NewRuntime(p.LibPath, ONNXRuntimeAPIVersion)
		if err != nil {
			p.initErr = fmt.Errorf("load onnxruntime (is it installed? 'brew install onnxruntime' on macOS): %w", err)
			return
		}
		env, err := rt.NewEnv("kvm-cli-local-grounding", ort.LoggingLevelError)
		if err != nil {
			p.initErr = fmt.Errorf("onnxruntime env: %w", err)
			return
		}
		session, err := rt.NewSession(env, p.ModelPath, &ort.SessionOptions{
			ExecutionProviders: p.EPs,
		})
		if err != nil {
			p.initErr = fmt.Errorf("load model %s: %w", p.ModelPath, err)
			return
		}
		p.rt, p.env, p.session = rt, env, session
	})
	return p.initErr
}

// openDecode reads and decodes an image file (JPEG or PNG).
func openDecode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

// Ground implements Provider.
func (p *LocalProvider) Ground(ctx context.Context, imagePath string, opts Options) (*Result, error) {
	start := time.Now()

	img, err := openDecode(imagePath)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	tensor, scale, padX, padY, err := yolo.Letterbox(img, TensorSize)
	if err != nil {
		return nil, err
	}
	var data []float32
	var shape []int64
	if p.runInference != nil {
		data, shape, err = p.runInference(ctx, tensor, []int64{1, 3, TensorSize, TensorSize})
		if err != nil {
			return nil, err
		}
	} else {
		if err := p.init(); err != nil {
			return nil, err
		}
		input, err := ort.NewTensorValue(p.rt, tensor, []int64{1, 3, TensorSize, TensorSize})
		if err != nil {
			return nil, fmt.Errorf("build input tensor: %w", err)
		}
		outputs, err := p.session.Run(ctx, map[string]*ort.Value{InputName: input})
		if err != nil {
			return nil, fmt.Errorf("onnxruntime inference: %w", err)
		}
		out, ok := outputs[OutputName]
		if !ok {
			return nil, fmt.Errorf("onnxruntime: model produced no %q output", OutputName)
		}
		data, shape, err = ort.GetTensorData[float32](out)
		if err != nil {
			return nil, fmt.Errorf("read output tensor: %w", err)
		}
	}
	dets, err := yolo.Decode(data, shape, opts.BoxThreshold)
	if err != nil {
		return nil, err
	}
	dets = yolo.NMS(dets, opts.IouThreshold)
	elements := yolo.ToElements(dets, scale, padX, padY, w, h)

	return &Result{
		Elements:  elements,
		Count:     len(elements),
		ElapsedMS: float64(time.Since(start).Microseconds()) / 1000.0,
		Backend:   BackendLocal,
		Width:     w,
		Height:    h,
		Model:     "icon_detect-local",
	}, nil
}

// Close releases the ONNX Runtime session/env (safe to call multiple times).
func (p *LocalProvider) Close() {
	p.once.Do(func() {}) // ensure init settled so the fields are final
	if p.env != nil {
		p.env.Close()
		p.env = nil
	}
	if p.rt != nil {
		p.rt.Close()
		p.rt = nil
	}
	p.session = nil
}
