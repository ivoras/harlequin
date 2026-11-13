package agent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/skills"
	"github.com/ivoras/harlequin/internal/server/userconfig"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// CheckHat reports an error when hat is non-empty and neither a shared hat nor
// one of the user's own.
func (a *Agent) CheckHat(ctx context.Context, userDB *sql.DB, hat string) error {
	if hat == "" {
		return nil
	}
	if _, err := a.Skills.GetHat(ctx, userDB, hat); err != nil {
		return fmt.Errorf("hat %q not found", hat)
	}
	return nil
}

// wearableHats lists the hats this session can wear: in a project session only
// the shared ones (personal hats are unreadable to other members).
func (a *Agent) wearableHats(ctx context.Context, userDB *sql.DB, projectID int64) []types.Hat {
	hats, err := a.Skills.ListHats(ctx, userDB)
	if err != nil || projectID == 0 {
		return hats
	}
	var out []types.Hat
	for _, h := range hats {
		if h.Scope != types.HatScopePersonal {
			out = append(out, h)
		}
	}
	return out
}

// SetSessionHat makes the session wear hat ("" = take it off). sessDB holds the
// session (the user's db, or the project's for a project session; projectID 0
// = personal). The hat must exist. The choice is remembered per user
// (hat.last, which new sessions start with) and pushed to every connected
// client of the session so their chips update.
func (a *Agent) SetSessionHat(ctx context.Context, userDB, sessDB *sql.DB, userID, projectID, sessionID int64, hat string) error {
	if hat != "" {
		h, err := a.Skills.GetHat(ctx, userDB, hat)
		if err != nil {
			return err
		}
		if projectID != 0 && h.Scope == types.HatScopePersonal {
			return skills.ErrPersonalHatInProject
		}
	}
	if err := a.Sessions.SetHat(ctx, sessDB, sessionID, hat); err != nil {
		return err
	}
	if err := userconfig.NewStore().Set(ctx, userDB, userconfig.KeyHatLast, hat); err != nil {
		return err
	}
	ev := docevents.Event{Op: docevents.HatChanged, Scope: "personal", UserID: userID, SessionID: sessionID, Hat: hat}
	if projectID != 0 {
		ev.Scope, ev.ProjectID = "project", projectID
	}
	a.DocEvents.Publish(ev)
	return nil
}
