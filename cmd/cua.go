package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/auth"
	"github.com/roboalchemist/kvm-cli/pkg/crop"
	"github.com/roboalchemist/kvm-cli/pkg/elements"
	"github.com/roboalchemist/kvm-cli/pkg/models"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/roboalchemist/kvm-cli/pkg/redact"
	"github.com/roboalchemist/kvm-cli/pkg/scratch"
	"github.com/roboalchemist/kvm-cli/pkg/ws"
	"github.com/spf13/cobra"
)

// cuaAnnotateScratch is the --annotate value that writes the Set-of-Mark PNG to
// a generated scratch path instead of an explicit destination.
const cuaAnnotateScratch = "auto"

// cua command flags. Each is package-global because cobra binds flag pointers;
// only one cua subcommand runs per process.
var (
	flagCuaModelsURL string
	flagCuaImage     string
	flagCuaModel     string
	flagCuaPlanner   string
	flagCuaBox       float64
	flagCuaIoU       float64
	flagCuaAnnotate  string
	flagCuaKeepImage bool
	flagCuaExecute   bool
	flagCuaYes       bool
)

// Selector flags shared by 'cua find', 'cua click' (selector mode) and
// 'cua wait'. They resolve elements deterministically, bypassing the planner.
var (
	flagSelText        string
	flagSelExact       bool
	flagSelRegex       string
	flagSelRegion      string
	flagSelIndex       int
	flagSelID          int
	flagSelNearest     string
	flagSelInteractive bool
	flagSelFrom        string
	flagSelAll         bool
	flagMapRegion      string
	flagMapScale       float64
)

// wait flags.
var (
	flagWaitGone     bool
	flagWaitMaxWait  time.Duration
	flagWaitInterval time.Duration
)

// ---- model/config resolution ------------------------------------------------

// cuaConfig returns the persisted config, or an empty one when unreadable. The
// cua settings are non-fatal: a missing/malformed config falls back to defaults.
func cuaConfig() *auth.Config {
	cfg, err := auth.LoadConfig()
	if err != nil || cfg == nil {
		return &auth.Config{}
	}
	return cfg
}

// resolveModelsURL applies the precedence
// --models-url flag > KVM_MODELS_URL > config models_url > default.
func resolveModelsURL() string {
	return cuaFirstNonEmpty(
		flagCuaModelsURL,
		os.Getenv("KVM_MODELS_URL"),
		cuaConfig().ModelsURL,
		models.DefaultBaseURL,
	)
}

// resolveGroundingModel applies the precedence
// --model flag > KVM_GROUNDING_MODEL > config grounding_model. An empty result
// means "auto": pick a running grounding model from the catalog (see
// cuaResolveGrounding), falling back to models.DefaultGroundingModel.
func resolveGroundingModel() string {
	return cuaFirstNonEmpty(
		flagCuaModel,
		os.Getenv("KVM_GROUNDING_MODEL"),
		cuaConfig().GroundingModel,
	)
}

// resolvePlannerModel applies the precedence
// --planner flag > KVM_PLANNER_MODEL > config planner_model > "" (auto: pick a
// running chat model from the catalog).
func resolvePlannerModel() string {
	return cuaFirstNonEmpty(
		flagCuaPlanner,
		os.Getenv("KVM_PLANNER_MODEL"),
		cuaConfig().PlannerModel,
	)
}

func cuaFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// cuaModelsClient builds a models-platform client. The grounding model is left
// to cuaResolveGrounding/cuaResolveModels so it can be auto-picked from the
// catalog; a configured planner model is carried on the client. Insecure
// follows the global --insecure flag.
func cuaModelsClient() *models.Client {
	return models.NewClient(models.Options{
		BaseURL:      resolveModelsURL(),
		PlannerModel: resolvePlannerModel(),
		Timeout:      cuaModelsTimeout(),
		Insecure:     flagInsecure,
	})
}

// cuaModelsTimeout uses the models platform's generous built-in default (120s,
// grounding is slow) unless the user explicitly set --timeout, which wins.
func cuaModelsTimeout() time.Duration {
	if rootCmd.PersistentFlags().Changed("timeout") {
		return flagTimeout
	}
	return 0
}

// cuaResolveGrounding returns the grounding model for this invocation. An
// explicit --model/KVM_GROUNDING_MODEL/config value wins; otherwise a running
// grounding model is auto-picked from the catalog (Catalog.DefaultGrounding),
// falling back to the built-in default (omniparser). It updates
// client.GroundingModel so the subsequent Probe/Ground/Parse call uses the
// resolved model.
func cuaResolveGrounding(ctx context.Context, client *models.Client) string {
	if g := resolveGroundingModel(); g != "" {
		client.GroundingModel = g
		return g
	}
	if cat, err := client.Catalog(ctx); err == nil {
		if g := cat.DefaultGrounding(); g != "" {
			client.GroundingModel = g
			return g
		}
	}
	return client.GroundingModel
}

// cuaResolveModels resolves the effective grounding and planner models in one
// shot. The catalog is fetched at most once, and only when a model is not
// already configured. grounding is always non-empty; planner may be empty when
// the catalog has no chat model (the caller decides whether that is fatal).
func cuaResolveModels(ctx context.Context, client *models.Client) (grounding, planner string, err error) {
	grounding = resolveGroundingModel()
	planner = resolvePlannerModel()
	if grounding != "" && planner != "" {
		client.GroundingModel = grounding
		return grounding, planner, nil
	}
	cat, cerr := client.Catalog(ctx)
	if cerr != nil {
		return "", "", cerr
	}
	if grounding == "" {
		grounding = cat.DefaultGrounding()
		if grounding == "" {
			grounding = models.DefaultGroundingModel
		}
	}
	if planner == "" {
		planner = cat.DefaultPlanner()
	}
	client.GroundingModel = grounding
	return grounding, planner, nil
}

// cuaRequirePlanner returns a coded error when no planner (chat) model is
// available to choose the element.
func cuaRequirePlanner(planner string) error {
	if planner != "" {
		return nil
	}
	return output.NewCodedError("DEVICE_ERROR",
		"no planner (chat) model is available on the models platform; set --planner or config planner_model")
}

// ---- annotated-image + screenshot helpers -----------------------------------

// cuaWriteAnnotated decodes a base64 PNG and writes it to explicit; the values
// "auto" and "-" (and a blank value) write to a generated scratch path. It is
// only called when --annotate was requested.
func cuaWriteAnnotated(b64, explicit, prefix, ext string) (string, error) {
	if strings.TrimSpace(b64) == "" {
		return "", output.NewCodedError("DEVICE_ERROR",
			"the grounding model returned no annotated image (it may not support Set-of-Mark output)")
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode annotated image: %w", err)
	}
	path := strings.TrimSpace(explicit)
	if path == "" || strings.EqualFold(path, cuaAnnotateScratch) || path == "-" {
		dir, err := ScratchDir()
		if err != nil {
			return "", err
		}
		path, err = scratch.PathIn(dir, prefix, ext)
		if err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// cuaCaptureScreenshot captures one frame into the scratch directory and
// returns its path. With a VNC target configured (--vnc / KVM_VNC_HOST) the
// frame is captured over VNC as PNG; otherwise a KVM frame is captured as JPEG.
// It never renders structured output, so it is safe inside a cua command whose
// stdout must stay parseable.
func cuaCaptureScreenshot(cmd *cobra.Command) (string, error) {
	dir, err := ScratchDir()
	if err != nil {
		return "", err
	}
	if vncTargetConfigured() {
		path, err := scratch.PathIn(dir, "kvm-cua", ".png")
		if err != nil {
			return "", err
		}
		png, _, _, err := VNCScreenshot(context.Background())
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(path, png, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Captured VNC screenshot %s (%d bytes)\n", path, len(png))
		return path, nil
	}

	path, err := scratch.PathIn(dir, "kvm-cua", ".jpg")
	if err != nil {
		return "", err
	}
	frames, _, err := shotGrabFrames(cmd, 1, false)
	if err != nil {
		return "", err
	}
	if len(frames) == 0 {
		return "", output.NewCodedError("DEVICE_ERROR", "no screenshot frame was captured")
	}
	if err := os.WriteFile(path, frames[0].Data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Captured screenshot %s (%d bytes)\n", path, len(frames[0].Data))
	return path, nil
}

// cuaResolveImage picks the image to operate on: an explicit positional or
// --image path, or a freshly captured scratch screenshot. The returned cleanup
// removes the captured screenshot unless --keep-image was set (default true).
func cuaResolveImage(cmd *cobra.Command, args []string, flagImage string) (string, func(), error) {
	pos := ""
	if len(args) > 0 {
		pos = strings.TrimSpace(args[0])
	}
	flagImage = strings.TrimSpace(flagImage)
	if pos != "" && flagImage != "" {
		return "", nil, output.NewCodedError("USAGE",
			"provide the image either as an argument or with --image, not both")
	}
	if path := cuaFirstNonEmpty(pos, flagImage); path != "" {
		return path, nil, nil
	}
	path, err := cuaCaptureScreenshot(cmd)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {}
	if !flagCuaKeepImage {
		cleanup = func() { _ = os.Remove(path) }
	}
	return path, cleanup, nil
}

// ---- selector / mapping helpers ---------------------------------------------

// cuaBuildQuery assembles an elements.Query from the shared selector flags and
// validates mutually exclusive combinations.
func cuaBuildQuery() (elements.Query, error) {
	q := elements.Query{Index: flagSelIndex}
	if flagSelID >= 0 {
		if flagSelIndex >= 0 {
			return q, output.NewCodedError("USAGE", "--id and --index are mutually exclusive")
		}
		q.Index = flagSelID
	}
	text := strings.TrimSpace(flagSelText)
	re := strings.TrimSpace(flagSelRegex)
	if text != "" && re != "" {
		return q, output.NewCodedError("USAGE", "--text and --regex are mutually exclusive")
	}
	if flagSelExact && text == "" {
		return q, output.NewCodedError("USAGE", "--exact requires --text")
	}
	q.Text = text
	q.Exact = flagSelExact
	q.Regex = re
	if s := strings.TrimSpace(flagSelRegion); s != "" {
		r, err := elements.ParseRegion(s)
		if err != nil {
			return q, output.NewCodedError("USAGE", err.Error())
		}
		q.Region = &r
	}
	if s := strings.TrimSpace(flagSelNearest); s != "" {
		p, err := elements.ParsePoint(s)
		if err != nil {
			return q, output.NewCodedError("USAGE", err.Error())
		}
		q.Nearest = p
	}
	q.Interactive = flagSelInteractive
	return q, nil
}

// cuaMapTransform returns the crop transform described by --map/--map-scale, or
// nil when no mapping was requested.
func cuaMapTransform() (*crop.Transform, error) {
	s := strings.TrimSpace(flagMapRegion)
	if s == "" {
		return nil, nil
	}
	r, err := elements.ParseRegion(s)
	if err != nil {
		return nil, output.NewCodedError("USAGE", err.Error())
	}
	scale := flagMapScale
	if scale <= 0 {
		scale = 1
	}
	return &crop.Transform{Region: r, Scale: scale}, nil
}

// cuaLoadElements returns grounded elements either from a saved ground JSON
// (--from) or by grounding the resolved image. annotated is the base64
// Set-of-Mark PNG when a fresh ground produced one, so callers can honour
// --annotate. A crop transform from --map/--map-scale is applied to every
// element's center and bbox before returning.
func cuaLoadElements(ctx context.Context, cmd *cobra.Command, client *models.Client, args []string, from, image string) (elems []models.Element, imagePath, annotated string, cleanup func(), err error) {
	tr, err := cuaMapTransform()
	if err != nil {
		return nil, "", "", nil, err
	}
	if strings.TrimSpace(from) != "" {
		data, rerr := os.ReadFile(from)
		if rerr != nil {
			return nil, "", "", nil, output.NewCodedError("USAGE", fmt.Sprintf("read --from %s: %v", from, rerr))
		}
		var g cuaGroundOutput
		if jerr := json.Unmarshal(data, &g); jerr != nil {
			return nil, "", "", nil, output.NewCodedError("USAGE", fmt.Sprintf("parse --from %s: %v", from, jerr))
		}
		elems := g.Elements
		if tr != nil {
			elems = crop.MapElements(elems, *tr)
		}
		return elems, g.ImagePath, "", func() {}, nil
	}

	imagePath, cleanup, err = cuaResolveImage(cmd, args, image)
	if err != nil {
		return nil, "", "", nil, err
	}
	cuaResolveGrounding(ctx, client)
	res, gerr := client.Ground(ctx, imagePath, models.GroundOptions{
		BoxThreshold: flagCuaBox,
		IouThreshold: flagCuaIoU,
	})
	if gerr != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, "", "", nil, gerr
	}
	elems = res.Elements
	if tr != nil {
		elems = crop.MapElements(elems, *tr)
	}
	return elems, imagePath, res.AnnotatedImage, cleanup, nil
}

// cuaFindMatch is the JSON shape of one selected element.
type cuaFindMatch struct {
	ID            int        `json:"id"`
	Type          string     `json:"type"`
	Content       string     `json:"content"`
	Interactivity bool       `json:"interactivity"`
	BBox          [4]float64 `json:"bbox"`
	Center        [2]float64 `json:"center"`
	Distance      float64    `json:"distance,omitempty"`
}

func cuaMatch(m elements.Match) cuaFindMatch {
	return cuaFindMatch{
		ID:            m.ID,
		Type:          m.Element.Type,
		Content:       m.Element.Content,
		Interactivity: m.Element.Interactivity,
		BBox:          m.Element.BBox,
		Center:        m.Element.Center,
		Distance:      m.Distance,
	}
}

// cuaSelectorOut echoes the selector used, for machine consumers.
type cuaSelectorOut struct {
	Text        string `json:"text,omitempty"`
	Exact       bool   `json:"exact,omitempty"`
	Regex       string `json:"regex,omitempty"`
	Region      string `json:"region,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Nearest     string `json:"nearest,omitempty"`
	Index       int    `json:"index"`
}

func cuaSelectorFromFlags() cuaSelectorOut {
	return cuaSelectorOut{
		Text:        flagSelText,
		Exact:       flagSelExact,
		Regex:       flagSelRegex,
		Region:      flagSelRegion,
		Interactive: flagSelInteractive,
		Nearest:     flagSelNearest,
		Index:       flagSelIndex,
	}
}

// ---- output shapes ----------------------------------------------------------

type cuaGroundOutput struct {
	ImagePath     string           `json:"image_path"`
	Model         string           `json:"model"`
	Width         int              `json:"width"`
	Height        int              `json:"height"`
	Count         int              `json:"count"`
	Elements      []models.Element `json:"elements"`
	ElapsedMS     float64          `json:"elapsed_ms"`
	AnnotatedPath string           `json:"annotated_path,omitempty"`
}

type cuaClickOutput struct {
	Instruction    string          `json:"instruction,omitempty"`
	ElementID      int             `json:"element_id"`
	ElementType    string          `json:"element_type"`
	ElementContent string          `json:"element_content"`
	ClickX         float64         `json:"click_x"`
	ClickY         float64         `json:"click_y"`
	ScreenshotPath string          `json:"screenshot_path"`
	GroundMS       float64         `json:"ground_ms"`
	PlanMS         float64         `json:"plan_ms"`
	Executed       bool            `json:"executed"`
	DryRun         bool            `json:"dry_run,omitempty"`
	AnnotatedPath  string          `json:"annotated_path,omitempty"`
	Selector       *cuaSelectorOut `json:"selector,omitempty"`
	MatchCount     int             `json:"match_count,omitempty"`
}

type cuaParseOutput struct {
	ImagePath     string                 `json:"image_path"`
	Count         int                    `json:"count"`
	Latency       float64                `json:"latency"`
	Content       []models.ParsedContent `json:"parsed_content_list"`
	AnnotatedPath string                 `json:"annotated_path,omitempty"`
}

type cuaCatalogModel struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Running     bool   `json:"running"`
	Default     bool   `json:"default"`
	DefaultRole string `json:"default_role,omitempty"`
}

type cuaModelsOutput struct {
	ModelsURL        string            `json:"models_url"`
	GroundingDefault string            `json:"grounding_default"`
	PlannerDefault   string            `json:"planner_default"`
	Models           []cuaCatalogModel `json:"models"`
}

type cuaProbeOutput struct {
	Message        string `json:"message"`
	ModelsURL      string `json:"models_url"`
	GroundingModel string `json:"grounding_model"`
	PlannerModel   string `json:"planner_model,omitempty"`
}

// ---- command group ----------------------------------------------------------

var cuaCmd = &cobra.Command{
	Use:   "cua",
	Short: "Computer-use assistance: ground, plan, and click through the models platform",
	Long: `Computer-use assistance (CUA) commands built on the personal models platform.

These commands turn a screenshot into a set-of-mark numbered element list
(OmniParser "grounding"), let a chat model choose the element matching an
instruction (the "planner"), and can optionally perform the resulting click on
the target over the HID WebSocket.

The element list — never image bytes — is what reaches the planner, so prompts
stay small and images stay out of the model context.

Configuration (flag > environment > config > auto-pick > default):
  --models-url / KVM_MODELS_URL / models_url        default https://models.example.com
  --model      / KVM_GROUNDING_MODEL / grounding_model   default auto (a running grounding model)
  --planner    / KVM_PLANNER_MODEL / planner_model       default auto (a running chat model)
  --scratch-dir / KVM_SCRATCH_DIR / scratch_dir          default OS temp dir

Run 'kvm-cli cua models' to see which grounding and chat models are available.`,
	Example: `  kvm-cli cua models
  kvm-cli cua probe
  kvm-cli cua ground
  kvm-cli cua ground screenshot.jpg --annotate /tmp/som.png
  kvm-cli cua click "click the Settings icon"
  kvm-cli cua parse screenshot.jpg`,
}

// ---- cua models -------------------------------------------------------------

var cuaModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List grounding and chat models on the models platform",
	Long: `Fetch the models platform catalog and list the grounding (screen-parser) and
chat (planner) models, with a running/available marker. The effective grounding
and planner defaults are marked with their role; --model/--planner (or their
env/config equivalents) override the auto-picked default.

The 'models_url' flag/env/config selects the platform root. Credentials embedded
in the URL (https://user:secret@host) are masked in the output.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli cua models
  kvm-cli cua models --json
  kvm-cli cua models --models-url https://models.example.com`,
	RunE: runCuaModels,
}

func runCuaModels(cmd *cobra.Command, args []string) error {
	client := cuaModelsClient()
	cat, err := client.Catalog(context.Background())
	if err != nil {
		return err
	}

	groundingDefault := resolveGroundingModel()
	if groundingDefault == "" {
		groundingDefault = cat.DefaultGrounding()
	}
	plannerDefault := resolvePlannerModel()
	if plannerDefault == "" {
		plannerDefault = cat.DefaultPlanner()
	}

	// The resolved URL can carry userinfo credentials
	// (https://user:secret@host); mask them so they never reach stdout.
	out := cuaModelsOutput{
		ModelsURL:        redact.Params(resolveModelsURL()),
		GroundingDefault: groundingDefault,
		PlannerDefault:   plannerDefault,
	}
	td := output.TableData{Headers: []string{"ID", "KIND", "RUNNING", "DEFAULT"}}

	relevant := append(cat.GroundingModels(), cat.ChatModels()...)
	for _, m := range relevant {
		role := cuaDefaultRole(m.ID, groundingDefault, plannerDefault)
		def := "-"
		if role != "" {
			def = role
		}
		td.Rows = append(td.Rows, []string{m.ID, m.Kind, strconv.FormatBool(m.Running()), def})
		out.Models = append(out.Models, cuaCatalogModel{
			ID:          m.ID,
			Kind:        m.Kind,
			Running:     m.Running(),
			Default:     role != "",
			DefaultRole: role,
		})
	}
	td.Footer = fmt.Sprintf("models_url: %s\ngrounding default: %s\nplanner default: %s",
		out.ModelsURL, out.GroundingDefault, emptyDash(out.PlannerDefault))
	return output.Render(td, out, GetOutputOptions())
}

// cuaDefaultRole describes how id relates to the effective defaults.
func cuaDefaultRole(id, grounding, planner string) string {
	role := ""
	if grounding != "" && id == grounding {
		role = "grounding"
	}
	if planner != "" && id == planner {
		if role == "" {
			role = "planner"
		} else {
			role += "+planner"
		}
	}
	return role
}

// ---- cua probe / status -----------------------------------------------------

var cuaProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Probe the grounding model and show the effective model settings",
	Long: `Call the grounding model's Microsoft omniparserserver probe endpoint and print
its readiness message together with the effective models_url, grounding_model
and planner_model resolved for this invocation. When no grounding/planner model
is configured, a running one is auto-picked from the catalog, so the reported
planner_model matches what 'cua click' would use. Credentials embedded in the
models_url are masked.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli cua probe
  kvm-cli cua probe --json`,
	RunE: runCuaProbe,
}

var cuaStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Alias for 'cua probe' (model readiness + effective settings)",
	Args:    cobra.NoArgs,
	Example: "  kvm-cli cua status\n  kvm-cli cua status --json",
	RunE:    runCuaProbe,
}

func runCuaProbe(cmd *cobra.Command, args []string) error {
	client := cuaModelsClient()
	ctx := context.Background()
	// Resolve the effective grounding and planner models (auto-picking a running
	// model when none is configured) so the report is not misleading.
	grounding, planner, err := cuaResolveModels(ctx, client)
	if err != nil {
		return err
	}
	msg, err := client.Probe(ctx)
	if err != nil {
		return err
	}
	out := cuaProbeOutput{
		Message:        msg,
		ModelsURL:      redact.Params(resolveModelsURL()),
		GroundingModel: grounding,
		PlannerModel:   planner,
	}
	td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
		{"message", out.Message},
		{"models_url", out.ModelsURL},
		{"grounding_model", out.GroundingModel},
		{"planner_model", emptyDash(out.PlannerModel)},
	}}
	return output.Render(td, out, GetOutputOptions())
}

// ---- cua ground -------------------------------------------------------------

var cuaGroundCmd = &cobra.Command{
	Use:   "ground [image]",
	Short: "Detect and number the UI elements in an image (OmniParser grounding)",
	Long: `Ground an image into a numbered list of UI elements.

With no image argument (and no --image) a fresh KVM screenshot is captured into
the scratch directory first. The image bytes are sent to the grounding model;
the response is an element list with each element's type, OCR content, pixel
bounding box, normalized bounding box and center point.

The annotated Set-of-Mark PNG is written only when --annotate is given (pass a
path, or --annotate auto to write to a scratch path). The base64 image is never
included in JSON output.

If a screenshot is captured for you it is kept in the scratch directory unless
--keep-image=false is passed.`,
	Args: cobra.MaximumNArgs(1),
	Example: `  kvm-cli cua ground
  kvm-cli cua ground screenshot.jpg
  kvm-cli cua ground --image /tmp/screen.jpg --annotate /tmp/som.png
  kvm-cli cua ground --box-threshold 0.1 --iou-threshold 0.2 --json`,
	RunE: runCuaGround,
}

func runCuaGround(cmd *cobra.Command, args []string) error {
	imagePath, cleanup, err := cuaResolveImage(cmd, args, flagCuaImage)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	annotate := strings.TrimSpace(flagCuaAnnotate) != ""
	client := cuaModelsClient()
	ctx := context.Background()
	cuaResolveGrounding(ctx, client)
	res, err := client.Ground(ctx, imagePath, models.GroundOptions{
		BoxThreshold:     flagCuaBox,
		IouThreshold:     flagCuaIoU,
		IncludeAnnotated: annotate,
	})
	if err != nil {
		return err
	}

	// Optional --region/--interactive filters keep the element list focused.
	elementsOut := res.Elements
	if strings.TrimSpace(flagSelRegion) != "" || flagSelInteractive {
		q, qerr := cuaBuildQuery()
		if qerr != nil {
			return qerr
		}
		ms, merr := elements.Select(res.Elements, elements.Query{
			Region: q.Region, Interactive: q.Interactive, Index: -1,
		})
		if merr != nil {
			return output.NewCodedError("USAGE", merr.Error())
		}
		elementsOut = make([]models.Element, len(ms))
		for i, m := range ms {
			elementsOut[i] = m.Element
		}
	}

	out := cuaGroundOutput{
		ImagePath: imagePath,
		Model:     res.Model,
		Width:     res.Width,
		Height:    res.Height,
		Count:     len(elementsOut),
		Elements:  elementsOut,
		ElapsedMS: res.ElapsedMS,
	}
	if annotate {
		path, err := cuaWriteAnnotated(res.AnnotatedImage, flagCuaAnnotate, "kvm-som", ".png")
		if err != nil {
			return err
		}
		out.AnnotatedPath = path
	}

	td := output.TableData{Headers: []string{"ID", "TYPE", "CONTENT", "CENTER"}}
	for i, e := range elementsOut {
		td.Rows = append(td.Rows, []string{
			strconv.Itoa(i),
			emptyDash(e.Type),
			cuaContent(e.Content),
			fmt.Sprintf("%.0f,%.0f", e.Center[0], e.Center[1]),
		})
	}
	footer := fmt.Sprintf("image: %s\ncount: %d\nelapsed_ms: %.1f", imagePath, out.Count, res.ElapsedMS)
	if out.AnnotatedPath != "" {
		footer += "\nannotated: " + out.AnnotatedPath
	}
	td.Footer = footer
	return output.Render(td, out, GetOutputOptions())
}

// ---- cua find ---------------------------------------------------------------

var cuaFindCmd = &cobra.Command{
	Use:   "find",
	Short: "Ground and select UI element(s) deterministically (no planner)",
	Long: `Ground a screenshot (or reuse elements from --from) and select element(s)
with deterministic criteria, without invoking the planner model.

Selectors (combined with AND; at least one is required):
  --text S          case-insensitive substring of the element's OCR content
  --exact           require --text to match the whole content
  --regex RE        case-insensitive regex against the content
  --interactive     only interactive elements
  --region X1,Y1,X2,Y2  only elements whose center is inside the rectangle
  --nearest X,Y     sort matches by distance to the point
  --index N         pick the Nth match (document order), or the element with id N when used alone

By default only the best match is printed; --all prints every match. The JSON
output contains each match's id, type, content, center, bbox and (with
--nearest) distance, plus click_x/click_y for the best match.

If the image is a crop produced by 'screenshot --region --scale', pass --map
X1,Y1,X2,Y2 and --map-scale to map element coordinates back to the full frame.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli cua find --text "Sign in"
  kvm-cli cua find --text "Next" --region 1300,150,1850,400
  kvm-cli cua find --regex "ok|cancel" --all
  kvm-cli cua find --index 4
  kvm-cli cua ground --json > /tmp/g.json && kvm-cli cua find --from /tmp/g.json --text Next`,
	RunE: runCuaFind,
}

type cuaFindOutput struct {
	ImagePath     string         `json:"image_path"`
	Selector      cuaSelectorOut `json:"selector"`
	MatchCount    int            `json:"match_count"`
	Matches       []cuaFindMatch `json:"matches"`
	Best          *cuaFindMatch  `json:"best,omitempty"`
	ClickX        float64        `json:"click_x,omitempty"`
	ClickY        float64        `json:"click_y,omitempty"`
	AnnotatedPath string         `json:"annotated_path,omitempty"`
}

func runCuaFind(cmd *cobra.Command, args []string) error {
	q, err := cuaBuildQuery()
	if err != nil {
		return err
	}
	if !q.HasSelector() {
		return output.NewCodedError("USAGE",
			"provide a selector: --text, --regex, --index, --region, --interactive or --nearest")
	}
	client := cuaModelsClient()
	ctx := context.Background()
	elems, imagePath, annB64, cleanup, err := cuaLoadElements(ctx, cmd, client, nil, flagSelFrom, flagCuaImage)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	matches, err := elements.Select(elems, q)
	if err != nil {
		return output.NewCodedError("USAGE", err.Error())
	}

	out := cuaFindOutput{
		ImagePath:  imagePath,
		Selector:   cuaSelectorFromFlags(),
		MatchCount: len(matches),
	}
	shown := matches
	if !flagSelAll && len(matches) > 1 {
		shown = matches[:1]
	}
	for _, m := range shown {
		out.Matches = append(out.Matches, cuaMatch(m))
	}
	if len(matches) > 0 {
		best := cuaMatch(matches[0])
		out.Best = &best
		out.ClickX, out.ClickY = best.Center[0], best.Center[1]
	}
	if strings.TrimSpace(flagCuaAnnotate) != "" && annB64 != "" {
		path, aerr := cuaWriteAnnotated(annB64, flagCuaAnnotate, "kvm-som", ".png")
		if aerr != nil {
			return aerr
		}
		out.AnnotatedPath = path
	}

	td := output.TableData{Headers: []string{"ID", "TYPE", "CONTENT", "CENTER", "DIST"}}
	for _, m := range out.Matches {
		dist := "-"
		if q.Nearest != nil {
			dist = fmt.Sprintf("%.0f", m.Distance)
		}
		td.Rows = append(td.Rows, []string{
			strconv.Itoa(m.ID),
			emptyDash(m.Type),
			cuaContent(m.Content),
			fmt.Sprintf("%.0f,%.0f", m.Center[0], m.Center[1]),
			dist,
		})
	}
	footer := fmt.Sprintf("image: %s\nmatches: %d", imagePath, len(matches))
	if out.Best != nil {
		footer += fmt.Sprintf("\nbest: id=%d click=%.0f,%.0f", out.Best.ID, out.ClickX, out.ClickY)
	}
	td.Footer = footer
	if err := output.Render(td, out, GetOutputOptions()); err != nil {
		return err
	}
	if len(matches) == 0 {
		return output.NewCodedError("NO_MATCH", "no UI element matched the selector")
	}
	return nil
}

// ---- cua text ---------------------------------------------------------------

var cuaTextCmd = &cobra.Command{
	Use:   "text",
	Short: "Ground an image and print only the OCR element list (no planner)",
	Long: `Ground a screenshot (or reuse elements from --from) and print the numbered
element list — id, type, OCR content and center — without any planner model and
without writing the annotated image. It is the fastest way to read the screen as
text, and the tab-separated (--plaintext) form is convenient for piping.

With no image argument (and no --image) a fresh KVM screenshot is captured into
the scratch directory first.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli cua text
  kvm-cli cua text --plaintext
  kvm-cli cua text --from /tmp/g.json --json
  kvm-cli cua text --region 1300,150,1850,400`,
	RunE: runCuaText,
}

type cuaTextOutput struct {
	ImagePath string           `json:"image_path"`
	Count     int              `json:"count"`
	Elements  []models.Element `json:"elements"`
}

func runCuaText(cmd *cobra.Command, args []string) error {
	client := cuaModelsClient()
	ctx := context.Background()
	elems, imagePath, _, cleanup, err := cuaLoadElements(ctx, cmd, client, nil, flagSelFrom, flagCuaImage)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(flagSelRegion) != "" || flagSelInteractive {
		q, qerr := cuaBuildQuery()
		if qerr != nil {
			return qerr
		}
		ms, merr := elements.Select(elems, elements.Query{Region: q.Region, Interactive: q.Interactive, Index: -1})
		if merr != nil {
			return output.NewCodedError("USAGE", merr.Error())
		}
		filtered := make([]models.Element, len(ms))
		for i, m := range ms {
			filtered[i] = m.Element
		}
		elems = filtered
	}

	out := cuaTextOutput{ImagePath: imagePath, Count: len(elems), Elements: elems}
	td := output.TableData{Headers: []string{"ID", "TYPE", "CONTENT", "CENTER"}}
	for i, e := range elems {
		td.Rows = append(td.Rows, []string{
			strconv.Itoa(i),
			emptyDash(e.Type),
			cuaContent(e.Content),
			fmt.Sprintf("%.0f,%.0f", e.Center[0], e.Center[1]),
		})
	}
	td.Footer = fmt.Sprintf("image: %s\ncount: %d", imagePath, len(elems))
	return output.Render(td, out, GetOutputOptions())
}

// ---- cua click --------------------------------------------------------------

var cuaClickCmd = &cobra.Command{
	Use:   "click [INSTRUCTION]",
	Short: "Resolve (or perform) a click, via the planner or a deterministic selector",
	Long: `Resolve a target to a screen coordinate and, optionally, click it.

Two modes:

  Planner mode (default): capture a screenshot (unless --image is given) ->
  ground it into numbered elements -> ask the planner chat model which element
  matches INSTRUCTION -> resolve that element's center point.

  Selector mode: pass a deterministic selector (--text/--regex/--index/--from/
  --region/--interactive/--nearest) and the planner is skipped entirely; the
  element is chosen locally. In selector mode INSTRUCTION is omitted. This is
  faster and fully deterministic when you already know the label.

--region/--interactive also act as a pre-filter on the element set handed to the
planner in planner mode.

By default nothing is clicked; the chosen element and click (x,y) are printed.
Pass --execute together with --yes to actually move the mouse and click the
element over the HID WebSocket. --execute without --yes is refused. --dry-run
gates only --execute: the resolution still runs and the target is printed.

Only the element text list reaches the planner; image bytes are never put in the
model prompt.`,
	Args: cobra.MaximumNArgs(1),
	Example: `  kvm-cli cua click "click the Settings icon"
  kvm-cli cua click --text "Sign in" --execute --yes
  kvm-cli cua click --text "Next" --region 1300,150,1850,400 --json
  kvm-cli cua click --from /tmp/g.json --index 4 --execute --yes
  kvm-cli cua click "click Login" --image /tmp/screen.jpg --json`,
	RunE: runCuaClick,
}

func runCuaClick(cmd *cobra.Command, args []string) error {
	q, err := cuaBuildQuery()
	if err != nil {
		return err
	}
	selectorMode := strings.TrimSpace(flagSelFrom) != "" || q.HasSelector()

	var instruction string
	if selectorMode {
		if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
			return output.NewCodedError("USAGE",
				"provide either an INSTRUCTION or a selector (--text/--regex/--index/--from), not both")
		}
	} else {
		if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
			return output.NewCodedError("USAGE",
				"provide an INSTRUCTION or a selector (--text/--regex/--index/--from)")
		}
		instruction = strings.TrimSpace(args[0])
	}

	// --dry-run gates only the destructive step; the resolution loop still runs.
	execute := flagCuaExecute && !flagDryRun
	if execute {
		if err := vmRequireYes(flagCuaYes, "move the remote mouse and click"); err != nil {
			return err
		}
	}

	client := cuaModelsClient()
	ctx := context.Background()
	annotate := strings.TrimSpace(flagCuaAnnotate) != ""

	if selectorMode {
		return cuaClickSelector(cmd, ctx, client, q, execute, annotate)
	}
	return cuaClickPlanner(cmd, ctx, client, instruction, q, execute, annotate)
}

// cuaClickSelector resolves the click deterministically from a selector.
func cuaClickSelector(cmd *cobra.Command, ctx context.Context, client *models.Client, q elements.Query, execute, annotate bool) error {
	start := time.Now()
	elems, imagePath, annB64, cleanup, err := cuaLoadElements(ctx, cmd, client, nil, flagSelFrom, flagCuaImage)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return err
	}
	matches, err := elements.Select(elems, q)
	if err != nil {
		return output.NewCodedError("USAGE", err.Error())
	}
	groundMS := float64(time.Since(start).Microseconds()) / 1000.0
	if len(matches) == 0 {
		return output.NewCodedError("NO_MATCH", "no UI element matched the selector")
	}
	best := cuaMatch(matches[0])
	sel := cuaSelectorFromFlags()
	out := cuaClickOutput{
		ElementID:      best.ID,
		ElementType:    best.Type,
		ElementContent: best.Content,
		ClickX:         best.Center[0],
		ClickY:         best.Center[1],
		ScreenshotPath: imagePath,
		GroundMS:       groundMS,
		DryRun:         flagDryRun,
		Selector:       &sel,
		MatchCount:     len(matches),
	}
	if annotate && annB64 != "" {
		path, aerr := cuaWriteAnnotated(annB64, flagCuaAnnotate, "kvm-som", ".png")
		if aerr != nil {
			return aerr
		}
		out.AnnotatedPath = path
	}
	if execute {
		if err := cuaExecuteClick(cmd, int(best.Center[0]), int(best.Center[1])); err != nil {
			return err
		}
		out.Executed = true
	}
	return cuaRenderClick(out)
}

// cuaClickPlanner runs the ground -> plan -> click loop.
func cuaClickPlanner(cmd *cobra.Command, ctx context.Context, client *models.Client, instruction string, q elements.Query, execute, annotate bool) error {
	imagePath, cleanup, err := cuaResolveImage(cmd, nil, flagCuaImage)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	_, planner, err := cuaResolveModels(ctx, client)
	if err != nil {
		return err
	}
	if err := cuaRequirePlanner(planner); err != nil {
		return err
	}

	ground, err := client.Ground(ctx, imagePath, models.GroundOptions{
		BoxThreshold:     flagCuaBox,
		IouThreshold:     flagCuaIoU,
		IncludeAnnotated: annotate,
	})
	if err != nil {
		return err
	}
	if len(ground.Elements) == 0 {
		return output.NewCodedError("DEVICE_ERROR",
			"grounding found no UI elements in the screenshot")
	}

	// --region/--interactive act as a pre-filter; remap the planner's index
	// back to the original grounding id.
	elems := ground.Elements
	origIDs := makeSeq(len(elems))
	if q.Region != nil || q.Interactive {
		ms, merr := elements.Select(elems, elements.Query{Region: q.Region, Interactive: q.Interactive, Index: -1})
		if merr != nil {
			return output.NewCodedError("USAGE", merr.Error())
		}
		elems = make([]models.Element, len(ms))
		origIDs = make([]int, len(ms))
		for i, m := range ms {
			elems[i] = m.Element
			origIDs[i] = m.ID
		}
		if len(elems) == 0 {
			return output.NewCodedError("NO_MATCH", "no UI element is in the requested region")
		}
	}

	plan, err := client.Plan(ctx, instruction, elems, models.PlanOptions{
		Model: planner,
		Count: ground.Count,
	})
	if err != nil {
		return err
	}

	x, y := plan.Element.Center[0], plan.Element.Center[1]
	out := cuaClickOutput{
		Instruction:    instruction,
		ElementID:      origIDs[plan.ElementID],
		ElementType:    plan.Element.Type,
		ElementContent: plan.Element.Content,
		ClickX:         x,
		ClickY:         y,
		ScreenshotPath: imagePath,
		GroundMS:       ground.ElapsedMS,
		PlanMS:         plan.LatencyMS,
		DryRun:         flagDryRun,
		MatchCount:     len(elems),
	}
	if annotate {
		path, aerr := cuaWriteAnnotated(ground.AnnotatedImage, flagCuaAnnotate, "kvm-som", ".png")
		if aerr != nil {
			return aerr
		}
		out.AnnotatedPath = path
	}
	if execute {
		if err := cuaExecuteClick(cmd, int(x), int(y)); err != nil {
			return err
		}
		out.Executed = true
	}
	return cuaRenderClick(out)
}

// cuaRenderClick renders a resolved click (shared by both modes).
func cuaRenderClick(out cuaClickOutput) error {
	rows := [][]string{
		{"element_id", strconv.Itoa(out.ElementID)},
		{"element_type", emptyDash(out.ElementType)},
		{"element_content", cuaContent(out.ElementContent)},
		{"click", fmt.Sprintf("%.0f,%.0f", out.ClickX, out.ClickY)},
		{"screenshot", out.ScreenshotPath},
		{"ground_ms", fmt.Sprintf("%.1f", out.GroundMS)},
		{"executed", strconv.FormatBool(out.Executed)},
	}
	if out.Instruction != "" {
		rows = append([][]string{{"instruction", out.Instruction}}, rows...)
	}
	if out.Selector != nil {
		rows = append(rows, []string{"match_count", strconv.Itoa(out.MatchCount)})
		rows = append(rows, []string{"plan_ms", fmt.Sprintf("%.1f", out.PlanMS)})
	} else {
		rows = append(rows, []string{"plan_ms", fmt.Sprintf("%.1f", out.PlanMS)})
	}
	if out.DryRun {
		rows = append(rows, []string{"dry_run", "true"})
	}
	if out.AnnotatedPath != "" {
		rows = append(rows, []string{"annotated", out.AnnotatedPath})
	}
	td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: rows}
	return output.Render(td, out, GetOutputOptions())
}

// makeSeq returns [0,1,...,n-1].
func makeSeq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// ---- cua wait ---------------------------------------------------------------

var cuaWaitCmd = &cobra.Command{
	Use:   "wait",
	Short: "Poll the screen until a selector matches (or, with --gone, stops matching)",
	Long: `Repeatedly capture a screenshot and ground it until the selector matches
(or, with --gone, until it no longer matches). This replaces the agent
sleep-and-recapture loop.

Wait on content with --text or --regex. --region/--interactive narrow the match.
The loop polls every --interval until --max-wait elapses; on success the matched
element is printed and the command exits 0, otherwise a TIMEOUT error is
returned.`,
	Args: cobra.NoArgs,
	Example: `  kvm-cli cua wait --text "Push notification sent"
  kvm-cli cua wait --text "Loading" --gone --max-wait 60s
  kvm-cli cua wait --regex "signed in|dashboard" --json`,
	RunE: runCuaWait,
}

type cuaWaitOutput struct {
	Found     bool          `json:"found"`
	Gone      bool          `json:"gone"`
	Polls     int           `json:"polls"`
	ElapsedMS float64       `json:"elapsed_ms"`
	Element   *cuaFindMatch `json:"element,omitempty"`
}

func runCuaWait(cmd *cobra.Command, args []string) error {
	q, err := cuaBuildQuery()
	if err != nil {
		return err
	}
	if !q.HasContentSelector() {
		return output.NewCodedError("USAGE", "wait requires --text or --regex")
	}
	maxWait := flagWaitMaxWait
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	interval := flagWaitInterval
	if interval <= 0 {
		interval = time.Second
	}
	client := cuaModelsClient()
	ctx := context.Background()
	start := time.Now()
	deadline := start.Add(maxWait)
	userImage := strings.TrimSpace(flagCuaImage) != ""

	polls := 0
	for {
		polls++
		elems, imagePath, _, _, lerr := cuaLoadElements(ctx, cmd, client, nil, "", flagCuaImage)
		if lerr != nil {
			return lerr
		}
		matches, serr := elements.Select(elems, q)
		// Remove only screenshots we captured ourselves; never delete a
		// user-provided --image.
		if !userImage && imagePath != "" {
			_ = os.Remove(imagePath)
		}
		if serr != nil {
			return output.NewCodedError("USAGE", serr.Error())
		}
		found := len(matches) > 0
		if found != flagWaitGone {
			out := cuaWaitOutput{Found: found, Gone: flagWaitGone, Polls: polls,
				ElapsedMS: float64(time.Since(start).Microseconds()) / 1000.0}
			if found {
				m := cuaMatch(matches[0])
				out.Element = &m
			}
			td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
				{"found", strconv.FormatBool(found)},
				{"gone", strconv.FormatBool(flagWaitGone)},
				{"polls", strconv.Itoa(polls)},
			}}
			if out.Element != nil {
				td.Rows = append(td.Rows, []string{"element_id", strconv.Itoa(out.Element.ID)})
				td.Rows = append(td.Rows, []string{"element_content", cuaContent(out.Element.Content)})
				td.Rows = append(td.Rows, []string{"click", fmt.Sprintf("%.0f,%.0f", out.Element.Center[0], out.Element.Center[1])})
			}
			return output.Render(td, out, GetOutputOptions())
		}
		if !time.Now().Before(deadline) {
			out := cuaWaitOutput{Found: found, Gone: flagWaitGone, Polls: polls,
				ElapsedMS: float64(time.Since(start).Microseconds()) / 1000.0}
			td := output.TableData{Headers: []string{"FIELD", "VALUE"}, Rows: [][]string{
				{"found", strconv.FormatBool(found)},
				{"polls", strconv.Itoa(polls)},
			}}
			if rerr := output.Render(td, out, GetOutputOptions()); rerr != nil {
				return rerr
			}
			return output.NewCodedError("TIMEOUT",
				fmt.Sprintf("condition not met within %s (%d polls)", maxWait, polls))
		}
		time.Sleep(interval)
	}
}

// cuaExecuteClick moves the remote mouse to (x,y) and left-clicks. With a VNC
// target configured it clicks over VNC; otherwise it reuses the exact HID
// WebSocket mechanism as 'hid mouse move' + 'hid mouse click'.
func cuaExecuteClick(cmd *cobra.Command, x, y int) error {
	if vncTargetConfigured() {
		c, err := vncDial()
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		return c.ClickAt(x, y, "left")
	}
	return hidDo(cmd, func(c *ws.Client) error {
		if err := c.MouseMovePixels(x, y); err != nil {
			return err
		}
		// Let the pointer settle before the button event.
		time.Sleep(80 * time.Millisecond)
		return c.MouseClick("left")
	})
}

// ---- cua parse --------------------------------------------------------------

var cuaParseCmd = &cobra.Command{
	Use:   "parse [image]",
	Short: "Parse an image with the Microsoft omniparserserver-compatible endpoint",
	Long: `Parse an image through the grounding model's /parse/ compatibility endpoint and
print the parsed content list (type, normalized bbox, OCR content).

With no image argument (and no --image) a fresh KVM screenshot is captured into
the scratch directory first. The Set-of-Mark PNG returned by the server is
written only when --annotate is given (a path, or --annotate auto for a scratch
path). If a screenshot is captured for you it is kept in the scratch directory
unless --keep-image=false is passed.`,
	Args: cobra.MaximumNArgs(1),
	Example: `  kvm-cli cua parse screenshot.jpg
  kvm-cli cua parse
  kvm-cli cua parse screenshot.jpg --annotate /tmp/som.png --json`,
	RunE: runCuaParse,
}

func runCuaParse(cmd *cobra.Command, args []string) error {
	imagePath, cleanup, err := cuaResolveImage(cmd, args, flagCuaImage)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	client := cuaModelsClient()
	ctx := context.Background()
	cuaResolveGrounding(ctx, client)
	res, err := client.Parse(ctx, imagePath)
	if err != nil {
		return err
	}

	out := cuaParseOutput{
		ImagePath: imagePath,
		Count:     len(res.ContentList),
		Latency:   res.Latency,
		Content:   res.ContentList,
	}
	if strings.TrimSpace(flagCuaAnnotate) != "" {
		path, err := cuaWriteAnnotated(res.SOMImageBase64, flagCuaAnnotate, "kvm-som", ".png")
		if err != nil {
			return err
		}
		out.AnnotatedPath = path
	}

	td := output.TableData{Headers: []string{"TYPE", "BBOX", "CONTENT"}}
	for _, c := range res.ContentList {
		td.Rows = append(td.Rows, []string{emptyDash(c.Type), cuaBBox(c.BBox), cuaContent(c.Content)})
	}
	footer := fmt.Sprintf("image: %s\ncount: %d\nlatency: %.3f", imagePath, out.Count, res.Latency)
	if out.AnnotatedPath != "" {
		footer += "\nannotated: " + out.AnnotatedPath
	}
	td.Footer = footer
	return output.Render(td, out, GetOutputOptions())
}

// ---- small formatting helpers -----------------------------------------------

// cuaContent renders empty OCR text as a visible placeholder.
func cuaContent(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

// cuaBBox formats a normalized xyxy box.
func cuaBBox(b [4]float64) string {
	return fmt.Sprintf("%.3f,%.3f,%.3f,%.3f", b[0], b[1], b[2], b[3])
}

// registerSelectorFlags adds the deterministic element-selector flags to cmd.
// withFrom adds --from (reuse a saved ground JSON); withAll adds --all. The
// --map/--map-scale pair is always added.
func registerSelectorFlags(cmd *cobra.Command, withFrom, withAll bool) {
	cmd.Flags().StringVar(&flagSelText, "text", "", "Select elements whose content contains this text (case-insensitive)")
	cmd.Flags().BoolVar(&flagSelExact, "exact", false, "Require --text to match the whole content")
	cmd.Flags().StringVar(&flagSelRegex, "regex", "", "Select elements whose content matches this regex (case-insensitive)")
	cmd.Flags().StringVar(&flagSelRegion, "region", "", "Only elements whose center is inside X1,Y1,X2,Y2")
	cmd.Flags().IntVar(&flagSelIndex, "index", -1, "Pick the Nth match, or the element with id N when used alone")
	cmd.Flags().IntVar(&flagSelID, "id", -1, "Select the grounding element with this id (alias for a bare --index)")
	cmd.Flags().StringVar(&flagSelNearest, "nearest", "", "Sort matches by distance to X,Y")
	cmd.Flags().BoolVar(&flagSelInteractive, "interactive", false, "Only interactive elements")
	if withFrom {
		cmd.Flags().StringVar(&flagSelFrom, "from", "", "Reuse elements from a saved 'cua ground --json' file")
	}
	if withAll {
		cmd.Flags().BoolVar(&flagSelAll, "all", false, "Print every match (default: best match only)")
	}
	cmd.Flags().StringVar(&flagMapRegion, "map", "", "Treat the image as a crop of source region X1,Y1,X2,Y2 and map coords back")
	cmd.Flags().Float64Var(&flagMapScale, "map-scale", 1, "Scale factor of the cropped image for --map")
}

// ---- wiring -----------------------------------------------------------------

func init() {
	// --models-url is shared by every cua subcommand.
	cuaCmd.PersistentFlags().StringVar(&flagCuaModelsURL, "models-url", "",
		"Models platform base URL (env: KVM_MODELS_URL; default https://models.example.com)")

	// --vnc/--vnc-username/--vnc-password let every cua subcommand run against a
	// VNC target instead of the KVM (screenshot + click over VNC).
	cuaCmd.PersistentFlags().StringVar(&flagVNCAddr, "vnc", "",
		"Drive a VNC target HOST[:PORT] instead of the KVM (env: KVM_VNC_HOST, VNC_HOST)")
	cuaCmd.PersistentFlags().StringVar(&flagVNCUser, "vnc-username", "",
		"VNC username for ARD auth (env: KVM_VNC_USERNAME, VNC_USERNAME)")
	cuaCmd.PersistentFlags().StringVar(&flagVNCPassword, "vnc-password", "",
		"VNC password (env: KVM_VNC_PASSWORD, VNC_PASSWORD)")

	// --model (the grounding model) is accepted by every subcommand that grounds
	// or reports the effective grounding model. An empty value auto-picks a
	// running grounding model from the catalog.
	for _, c := range []*cobra.Command{cuaModelsCmd, cuaProbeCmd, cuaStatusCmd, cuaGroundCmd, cuaClickCmd, cuaFindCmd, cuaWaitCmd, cuaTextCmd} {
		c.Flags().StringVar(&flagCuaModel, "model", "",
			"Grounding (OmniParser) model id (env: KVM_GROUNDING_MODEL; default: auto)")
	}

	// --planner is used by click (to choose the element) and reported by
	// probe/models/status. It is not accepted by ground/parse, which never plan.
	for _, c := range []*cobra.Command{cuaModelsCmd, cuaProbeCmd, cuaStatusCmd, cuaClickCmd} {
		c.Flags().StringVar(&flagCuaPlanner, "planner", "",
			"Planner chat model id (env: KVM_PLANNER_MODEL; default: auto)")
	}

	// Thresholds apply to every grounding path.
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaClickCmd, cuaFindCmd, cuaWaitCmd, cuaTextCmd} {
		c.Flags().Float64Var(&flagCuaBox, "box-threshold", 0.05, "OmniParser box confidence threshold")
		c.Flags().Float64Var(&flagCuaIoU, "iou-threshold", 0.1, "OmniParser IoU threshold")
	}

	// --image is shared by ground, click, find, text and parse (wait always
	// captures unless --image is given).
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaClickCmd, cuaFindCmd, cuaTextCmd, cuaWaitCmd, cuaParseCmd} {
		c.Flags().StringVar(&flagCuaImage, "image", "", "Image file to operate on (default: capture a fresh screenshot)")
	}

	// --annotate writes the Set-of-Mark PNG to the given path ("auto" writes to
	// a generated scratch path).
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaClickCmd, cuaFindCmd, cuaParseCmd} {
		c.Flags().StringVar(&flagCuaAnnotate, "annotate", "",
			"Write the annotated Set-of-Mark PNG to PATH (use 'auto' for a scratch path)")
	}

	// --keep-image is shared by every command that can capture a screenshot for
	// you. pflag appends the "(default true)" suffix automatically, so the help
	// text must not repeat it.
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaClickCmd, cuaFindCmd, cuaParseCmd} {
		c.Flags().BoolVar(&flagCuaKeepImage, "keep-image", true,
			"Keep a screenshot captured for you in the scratch dir")
	}

	// Deterministic selector flags.
	registerSelectorFlags(cuaFindCmd, true, true)
	registerSelectorFlags(cuaClickCmd, true, false)
	registerSelectorFlags(cuaWaitCmd, false, false)

	// ground and text support the region/interactive filters; text also reuses
	// a saved ground via --from.
	for _, c := range []*cobra.Command{cuaGroundCmd, cuaTextCmd} {
		c.Flags().StringVar(&flagSelRegion, "region", "", "Only elements whose center is inside X1,Y1,X2,Y2")
		c.Flags().BoolVar(&flagSelInteractive, "interactive", false, "Only interactive elements")
	}
	cuaTextCmd.Flags().StringVar(&flagSelFrom, "from", "", "Reuse elements from a saved 'cua ground --json' file")

	// wait flags.
	cuaWaitCmd.Flags().BoolVar(&flagWaitGone, "gone", false, "Succeed when the selector stops matching")
	cuaWaitCmd.Flags().DurationVar(&flagWaitMaxWait, "max-wait", 30*time.Second, "Give up after this long")
	cuaWaitCmd.Flags().DurationVar(&flagWaitInterval, "interval", time.Second, "Poll interval")

	// click execution is destructive: gate it behind --yes/-f/--force. The
	// command is deliberately NOT annotated as a write, because its read-only
	// resolution path is safe and --dry-run must still resolve the click; the
	// destructive step is gated locally in runCuaClick (see execute).
	cuaClickCmd.Flags().BoolVar(&flagCuaExecute, "execute", false, "Move the mouse and click the resolved element (requires --yes)")
	vmRegisterConfirm(cuaClickCmd, &flagCuaYes, "Confirm moving and clicking the remote mouse (required with --execute)")

	cuaCmd.AddCommand(cuaModelsCmd, cuaProbeCmd, cuaStatusCmd, cuaGroundCmd, cuaClickCmd, cuaFindCmd, cuaWaitCmd, cuaTextCmd, cuaParseCmd)
	rootCmd.AddCommand(cuaCmd)
}
