package ui

import (
	"context"
	"net/http"
	"strings"

	"github.com/prippa/mail-sort/internal/engine"
)

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	if profile == "" {
		s.fail(w, http.StatusBadRequest, "config: profile name is empty")
		return
	}
	view, err := s.evaluateView(r.Context(), profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) evaluateView(ctx context.Context, profile string) (map[string]any, error) {
	recent, err := s.db.RecentExamples(ctx, profile, 40)
	if err != nil {
		return nil, err
	}
	labeled, err := s.db.LabeledExamples(ctx, profile, 500)
	if err != nil {
		return nil, err
	}
	suggestion := engine.SuggestThreshold(labeled)
	rows := make([]map[string]any, 0, len(recent))
	for _, item := range recent {
		rows = append(rows, map[string]any{
			"id":         item.ID,
			"uid":        item.UID,
			"subject":    item.Subject,
			"from":       item.From,
			"predicted":  item.Predicted,
			"category":   item.Category,
			"confidence": item.Confidence,
			"status":     item.Status,
			"override":   item.Override,
		})
	}
	return map[string]any{
		"rows": rows,
		"suggestion": map[string]any{
			"ok":        suggestion.OK,
			"threshold": suggestion.Threshold,
			"labeled":   suggestion.Labeled,
			"auto":      suggestion.Auto,
			"wrong":     suggestion.Wrong,
		},
	}, nil
}

type labelBody struct {
	Profile  string `json:"profile"`
	UID      uint32 `json:"uid"`
	Category string `json:"category"`
}

func (s *Server) labelExample(w http.ResponseWriter, r *http.Request) {
	var body labelBody
	if !s.readJSON(w, r, &body) {
		return
	}
	profile := strings.TrimSpace(body.Profile)
	if profile == "" {
		s.fail(w, http.StatusBadRequest, "config: profile name is empty")
		return
	}
	if body.UID == 0 {
		s.fail(w, http.StatusBadRequest, "engine: that message is not in the dry run")
		return
	}
	cfg, err := s.loadConfig(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	set, err := s.mustCategories(cfg)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	if _, err := engine.Label(r.Context(), s.db, profile, body.UID, set.Categories, strings.TrimSpace(body.Category)); err != nil {
		s.fail(w, http.StatusBadRequest, s.engineMessage(cfg, err))
		return
	}
	view, err := s.evaluateView(r.Context(), profile)
	if err != nil {
		s.fail(w, http.StatusBadRequest, s.publicError(cfg, err))
		return
	}
	writeJSON(w, http.StatusOK, view)
}
