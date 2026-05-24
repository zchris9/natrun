package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"natrun/internal/envfile"
	"natrun/internal/model"
	"natrun/internal/promptio"
	"natrun/internal/signing"
)

// reqCounter gives each /act request a short id so concurrent requests
// don't interleave in the logs.
var reqCounter atomic.Uint64

// --- types ---

type contextBlob struct {
	Visible map[string]any `json:"visible"`
	Sig     string         `json:"sig"`
}

type actRequest struct {
	Action  map[string]any `json:"action"`
	Context contextBlob    `json:"context"`
}

type actResponse struct {
	HTML    string      `json:"html"`
	Context contextBlob `json:"context"`
}

// --- server state ---

type server struct {
	secret       []byte
	systemPrompt string
	modelFirst   *model.Client
	modelNext    *model.Client
	shellHTML    []byte
	loaderJS     []byte
}

// --- asset and config loading ---

func loadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func loadSystemPrompt() (string, error) {
	site := os.Getenv("NATRUN_SITE")
	if site == "" {
		site = "default"
	}
	path := filepath.Join("examples", site+".nl")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("loading system prompt from %s: %w", path, err)
	}
	return string(b), nil
}

// --- context blob helpers ---

// The carried context c_n is, per the README, the running transcript of
// alternating user actions and model replies. We represent it as
//
//	{ "history": [ { "action": i_n, "reply": <raw model reply text> }, ... ] }
//
// where each entry is one turn of the conversation. The model's reply rides
// forward verbatim into the next turn's input, so the model sees its own
// prior outputs literally — which is what makes c_n the literal transcript
// the equations describe.
func emptyVisible() map[string]any {
	return map[string]any{
		"history": []any{},
	}
}

func (s *server) makeContext(visible map[string]any) (contextBlob, error) {
	sig, err := signing.Sign(s.secret, visible)
	if err != nil {
		return contextBlob{}, err
	}
	return contextBlob{Visible: visible, Sig: sig}, nil
}

// --- LLM message construction ---

// buildMessages turns the carried transcript + the new action into the
// {user, assistant, user, assistant, ..., user} pattern the API expects.
//
// Each prior turn in c_{n-1} contributes:
//   - one user message: the action i_n that drove that turn, as JSON
//   - one assistant message: the model's raw reply for that turn, verbatim
//
// This is the literal expansion of c_{n-1} ∥ i_n from the README. The
// model sees its own prior outputs unedited, which preserves design coherence
// across turns at the cost of growing input tokens.
func buildMessages(visible map[string]any, newAction map[string]any) []model.Message {
	var msgs []model.Message

	if hist, ok := visible["history"].([]any); ok {
		for _, raw := range hist {
			turn, _ := raw.(map[string]any)
			if turn == nil {
				continue
			}
			actJSON, _ := json.Marshal(turn["action"])
			msgs = append(msgs, model.Message{
				Role:    "user",
				Content: "user action: " + string(actJSON),
			})
			reply, _ := turn["reply"].(string)
			msgs = append(msgs, model.Message{
				Role:    "assistant",
				Content: reply,
			})
		}
	}

	actJSON, _ := json.Marshal(newAction)
	msgs = append(msgs, model.Message{
		Role:    "user",
		Content: "user action: " + string(actJSON),
	})
	return msgs
}

// --- handlers ---

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		log.Printf("404 %s %s from=%s", r.Method, r.URL.Path, r.RemoteAddr)
		http.NotFound(w, r)
		return
	}
	log.Printf("GET / from=%s ua=%q", r.RemoteAddr, truncate(r.UserAgent(), 80))

	visible := emptyVisible()
	ctx, err := s.makeContext(visible)
	if err != nil {
		log.Printf("GET / sign fail: %v", err)
		http.Error(w, "signing error", 500)
		return
	}
	ctxJSON, _ := json.Marshal(ctx)

	out := string(s.shellHTML)
	out = strings.Replace(out, "{{INITIAL_HTML}}", initialHTML(), 1)
	out = strings.Replace(out, "{{INITIAL_CONTEXT}}", string(ctxJSON), 1)
	out = strings.Replace(out, "{{LOADER_JS}}", string(s.loaderJS), 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(out))
	log.Printf("GET / ok shell_bytes=%d sig=%s", len(out), shortSig(ctx.Sig))
}

// initialHTML is the placeholder shown for a fraction of a second between
// page load and the first /act response. The loader auto-fires {"type":"open"}
// on load, so the user never sees this for long.
// initialHTML is what fills #natrun-root for the ~50ms between the shell
// rendering and the loader's first /act response. Empty by design: the model
// owns all visuals, and we don't want a hardcoded flash that contradicts the
// site it's about to render. If the model's first response is slow, the page
// briefly looks blank — that's fine. The natrun-busy class on #natrun-root is
// still set during the wait; the model can style it if it wants a visible
// "loading" indicator on first paint.
func initialHTML() string {
	return ""
}

func (s *server) handleAct(w http.ResponseWriter, r *http.Request) {
	id := reqCounter.Add(1)
	tag := fmt.Sprintf("[#%d]", id)
	start := time.Now()

	if r.Method != http.MethodPost {
		log.Printf("%s reject: method %s", tag, r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req actRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("%s reject: bad json: %v", tag, err)
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	turn := historyLen(req.Context.Visible) + 1
	actJSON, _ := json.Marshal(req.Action)
	log.Printf("%s act turn=%d from=%s action=%s sig=%s",
		tag, turn, r.RemoteAddr, truncate(string(actJSON), 200), shortSig(req.Context.Sig))

	// Verify HMAC. Reject tampered or unsigned context.
	if err := signing.Verify(s.secret, req.Context.Visible, req.Context.Sig); err != nil {
		log.Printf("%s sig fail: %v", tag, err)
		http.Error(w, "bad context signature", http.StatusBadRequest)
		return
	}
	log.Printf("%s sig ok", tag)

	// Pick the model: first turn vs. consecutive.
	mc := s.modelNext
	slot := "next"
	if historyLen(req.Context.Visible) == 0 {
		mc = s.modelFirst
		slot = "first"
	}

	// Build the model call.
	msgs := buildMessages(req.Context.Visible, req.Action)
	log.Printf("%s model send slot=%s model=%s messages=%d max_tokens=%d",
		tag, slot, mc.Model, len(msgs), mc.MaxTokens)

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	modelStart := time.Now()
	reply, err := mc.Send(ctx, s.systemPrompt, msgs)
	modelDur := time.Since(modelStart)
	if err != nil {
		log.Printf("%s model fail dur=%s err=%v", tag, modelDur.Round(time.Millisecond), err)
		s.writeFallback(w, req, fmt.Sprintf("the model could not respond (%s).", err.Error()))
		return
	}
	outRatio := 0.0
	if mc.MaxTokens > 0 {
		outRatio = float64(reply.Usage.OutputTokens) / float64(mc.MaxTokens) * 100
	}
	stopNote := ""
	if reply.StopReason == "max_tokens" {
		stopNote = " (HIT CAP)"
	}
	log.Printf("%s model ok dur=%s reply_bytes=%d in_tok=%d out_tok=%d/%d (%.1f%%)%s stop=%s",
		tag, modelDur.Round(time.Millisecond), len(reply.Text),
		reply.Usage.InputTokens, reply.Usage.OutputTokens, mc.MaxTokens, outRatio, stopNote,
		reply.StopReason)

	parsed, err := promptio.Parse(reply.Text)
	if err != nil {
		log.Printf("%s parse fail err=%v reply=%q", tag, err, truncate(reply.Text, 400))
		s.writeFallback(w, req, "the model returned something natrun could not parse.")
		return
	}
	log.Printf("%s parse ok html_bytes=%d", tag, len(parsed.HTML))

	// Extend the transcript c_n = c_{n-1} ∥ i_n ∥ reply by appending
	// {action, reply} to history. The reply is the model's raw text — it
	// goes into the assistant slot when buildMessages reconstructs the
	// chat for the next turn.
	newVisible := appendHistory(req.Context.Visible, req.Action, reply.Text)

	newCtx, err := s.makeContext(newVisible)
	if err != nil {
		log.Printf("%s sign fail: %v", tag, err)
		http.Error(w, "signing error", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := actResponse{HTML: parsed.HTML, Context: newCtx}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("%s write fail: %v", tag, err)
		return
	}
	log.Printf("%s ok total=%s", tag, time.Since(start).Round(time.Millisecond))
}

// historyLen returns the number of turns already in the carried context.
func historyLen(visible map[string]any) int {
	if hist, ok := visible["history"].([]any); ok {
		return len(hist)
	}
	return 0
}

// shortSig returns the first 8 chars of a signature for logging.
func shortSig(sig string) string {
	if sig == "" {
		return "(empty)"
	}
	if len(sig) <= 8 {
		return sig
	}
	return sig[:8] + "…"
}

func appendHistory(visible map[string]any, action map[string]any, reply string) map[string]any {
	hist, _ := visible["history"].([]any)
	hist = append(hist, map[string]any{
		"action": action,
		"reply":  reply,
	})
	return map[string]any{
		"history": hist,
	}
}

// writeFallback returns a graceful failure page. It keeps the carried context
// so the user can retry the same action without losing history.
func (s *server) writeFallback(w http.ResponseWriter, req actRequest, reason string) {
	html := fmt.Sprintf(`<div style="max-width:42rem;margin:4rem auto;line-height:1.6;opacity:0.85">
  <h2 style="font-weight:300">the page glitched</h2>
  <p style="opacity:0.7">%s</p>
  <p><button data-natrun-action='{"type":"retry"}'
       style="background:#1a1a1d;color:#e9e9ea;border:1px solid #333;padding:0.5rem 1rem;border-radius:6px;cursor:pointer;font-family:inherit">
       try again
     </button></p>
</div>`, htmlEscape(reason))

	// Echo the carried (still-valid) context back unchanged.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(actResponse{
		HTML:    html,
		Context: req.Context,
	})
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// --- main ---

func main() {
	if err := envfile.Load(".env"); err != nil {
		log.Printf("warning: reading .env: %v", err)
	}

	secret := os.Getenv("NATRUN_SECRET")
	if secret == "" {
		log.Fatal("NATRUN_SECRET is empty — set it in .env")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is empty — set it in .env")
	}

	shellHTML, err := loadFile("web/index.html")
	if err != nil {
		log.Fatalf("loading web/index.html: %v", err)
	}
	loaderJS, err := loadFile("web/loader.js")
	if err != nil {
		log.Fatalf("loading web/loader.js: %v", err)
	}
	systemPrompt, err := loadSystemPrompt()
	if err != nil {
		log.Fatal(err)
	}

	modelFirst := model.NewWith(apiKey,
		defaultIfEmpty(os.Getenv("NATRUN_MODEL_FIRST"), "claude-opus-4-7"),
		envInt("NATRUN_MAX_TOK_FIRST", 8192))
	modelNext := model.NewWith(apiKey,
		defaultIfEmpty(os.Getenv("NATRUN_MODEL_NEXT"), "claude-haiku-4-5"),
		envInt("NATRUN_MAX_TOK_NEXT", 8192))

	s := &server{
		secret:       []byte(secret),
		systemPrompt: systemPrompt,
		modelFirst:   modelFirst,
		modelNext:    modelNext,
		shellHTML:    shellHTML,
		loaderJS:     loaderJS,
	}

	addr := os.Getenv("NATRUN_ADDR")
	if addr == "" {
		addr = ":8000"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/act", s.handleAct)

	site := defaultIfEmpty(os.Getenv("NATRUN_SITE"), "default")
	log.Printf("natrun starting")
	log.Printf("  addr            = %s", addr)
	log.Printf("  site            = %s", site)
	log.Printf("  prompt bytes    = %d", len(systemPrompt))
	log.Printf("  shell bytes     = %d", len(shellHTML))
	log.Printf("  loader bytes    = %d", len(loaderJS))
	log.Printf("  model (first)   = %s (max_tokens=%d)", modelFirst.Model, modelFirst.MaxTokens)
	log.Printf("  model (next)    = %s (max_tokens=%d)", modelNext.Model, modelNext.MaxTokens)
	log.Printf("  secret bytes    = %d", len(secret))
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func defaultIfEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func envInt(key string, d int) int {
	v := os.Getenv(key)
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("warning: %s=%q is not a positive integer, using default %d", key, v, d)
		return d
	}
	return n
}
