package pool

import (
	"context"
	"fmt"
	"strings"

	"github.com/shirou-eh/notiongate/internal/model"
)

// AddInput describes a new account to bootstrap and add to the pool.
type AddInput struct {
	TokenV2    string  `json:"token_v2"`
	Label      string  `json:"label,omitempty"`
	Proxy      string  `json:"proxy,omitempty"`
	LimitReq   int64   `json:"limit_req,omitempty"`
	WindowType string  `json:"window_type,omitempty"`
	RotateAt   float64 `json:"rotate_at,omitempty"`
	// Force adds the account even if bootstrap could not discover
	// user_id/space_id (Notion API shape may have changed).
	Force bool `json:"force,omitempty"`
}

// AddWithBootstrap discovers account metadata via Notion and stores it.
func (p *Pool) AddWithBootstrap(ctx context.Context, in AddInput) (model.Account, error) {
	in.TokenV2 = strings.TrimSpace(in.TokenV2)
	if in.TokenV2 == "" {
		return model.Account{}, fmt.Errorf("token_v2 is required")
	}
	acc := model.Account{
		ID:         model.NewID(),
		Label:      in.Label,
		TokenV2:    in.TokenV2,
		Proxy:      in.Proxy,
		LimitReq:   in.LimitReq,
		WindowType: in.WindowType,
		RotateAt:   in.RotateAt,
	}
	if acc.WindowType != model.WindowDay && acc.WindowType != model.WindowMonth {
		acc.WindowType = p.cfg.DefaultWindow
	}
	if acc.RotateAt <= 0 || acc.RotateAt > 1 {
		acc.RotateAt = p.cfg.RotateAt
	}

	info, err := p.BootstrapToken(ctx, acc)
	if err != nil && !in.Force {
		return model.Account{}, fmt.Errorf("bootstrap failed (use force=true to add anyway): %w", err)
	}
	if info != nil {
		acc.UserID = info.UserID
		acc.SpaceID = info.SpaceID
		acc.SpaceViewID = info.SpaceViewID
		acc.SpaceName = info.SpaceName
		acc.Email = info.Email
		acc.BaseURL = info.BaseURL // pinned notion.so/.com that accepted the token
		if len(info.Models) > 0 {
			acc.Models = info.Models
		}
	}
	if acc.Label == "" {
		if acc.Email != "" {
			acc.Label = acc.Email
		} else {
			acc.Label = acc.MaskedToken()
		}
	}
	if err := p.Add(acc); err != nil {
		return model.Account{}, err
	}
	return acc, nil
}
