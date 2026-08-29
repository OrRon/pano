package control

import (
	"context"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/bus"
	"github.com/orron/pano/internal/flow"
)

// Backend is what the daemon exposes to the control API.
type Backend interface {
	Status(ctx context.Context) api.Status
	Stats(ctx context.Context) api.Stats
	Capture(ctx context.Context, req api.CaptureRequest) (api.Status, error)

	Sessions(ctx context.Context) ([]api.Session, error)
	StartSession(ctx context.Context, name string) (api.Session, error)
	DeleteSession(ctx context.Context, id string) error

	ListFlows(ctx context.Context, f api.FlowFilter) (api.FlowList, error)
	GetFlow(ctx context.Context, id flow.ID, q api.FlowQuery) (api.FlowDetail, error)
	GetFlowRaw(ctx context.Context, id flow.ID) (*flow.Flow, error)
	Body(ctx context.Context, id flow.ID, part string, decode bool) (data []byte, mime string, err error)
	WSMessages(ctx context.Context, id flow.ID, limit int) ([]flow.WSMessage, error)
	Replay(ctx context.Context, id flow.ID, req api.ReplayRequest) (api.ReplayResult, error)
	Diff(ctx context.Context, req api.DiffRequest) (api.DiffResult, error)
	Explain(ctx context.Context, id flow.ID, req api.ExplainRequest) (api.ExplainResult, error)
	Tail(ctx context.Context, req api.TailRequest) (api.TailResult, error)
	Bus() *bus.Bus

	Rules(ctx context.Context) []api.Rule
	AddRule(ctx context.Context, req api.RuleAddRequest) (api.Rule, error)
	UpdateRule(ctx context.Context, id string, p api.RulePatch) (api.Rule, error)
	RemoveRule(ctx context.Context, id string) error
	RemoveAllRules(ctx context.Context) int
	Presets(ctx context.Context) []api.Preset

	Held(ctx context.Context) []api.Held
	Resume(ctx context.Context, id flow.ID, req api.ResumeRequest) error

	Decrypt(ctx context.Context) api.Decrypt
	ChangeDecrypt(ctx context.Context, c api.DecryptChange) (api.Decrypt, error)

	HAR(ctx context.Context, req api.HARRequest) (api.HARResult, error)
	CAPEM(ctx context.Context) []byte

	SysProxy(ctx context.Context) api.SysProxy
	SetSysProxy(ctx context.Context, req api.SysProxyRequest) (api.SysProxy, error)

	Mobile(ctx context.Context) api.Mobile
	SetMobile(ctx context.Context, req api.MobileRequest) (api.Mobile, error)

	Config(ctx context.Context) any
	Shutdown(ctx context.Context) error
	Audit(line string)
}
