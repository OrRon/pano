package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/store"
)

func addFilterFlags(cmd *cobra.Command, f *api.FlowFilter) {
	fl := cmd.Flags()
	fl.StringVar(&f.Q, "q", "", "full-text search (url, headers, text bodies)")
	fl.StringVar(&f.Host, "host", "", "host glob, e.g. api.openai.com or *.googleapis.com")
	fl.StringVar(&f.Path, "path", "", "path prefix or glob")
	fl.StringSliceVarP(&f.Method, "method", "m", nil, "methods (GET,POST)")
	fl.StringVar(&f.Status, "status", "", "status: 500, 4xx, 400-499, !2xx")
	fl.StringVar(&f.Since, "since", "", "RFC3339, relative (15m, 2h, 1d) or a flow id")
	fl.StringVar(&f.Until, "until", "", "RFC3339 or relative")
	fl.StringVar(&f.ContentType, "type", "", "json|sse|html|js|css|img|bin|text or MIME prefix")
	fl.Int64Var(&f.MinBytes, "min-bytes", 0, "minimum total bytes")
	fl.BoolVar(&f.HasError, "errors", false, "only errors (status ≥ 400 or transport failure)")
	fl.StringVar(&f.Tag, "tag", "", "tag")
	fl.StringVar(&f.Rule, "rule", "", "rule id that matched")
	fl.StringVar(&f.State, "state", "", "all|held|active|done|failed|replayed|mocked|blocked")
	fl.StringVar(&f.Kind, "kind", "", "http|websocket|tunnel")
	fl.StringVar(&f.Session, "session", "", "session id")
	fl.StringVar(&f.Client, "client", "", "client IP (a phone from `pano mobile status`), or 'remote' for every non-loopback client")
}

func (a *App) cmdFlows() *cobra.Command {
	var f api.FlowFilter
	cmd := &cobra.Command{
		Use:     "flows",
		Aliases: []string{"ls"},
		Short:   "List captured flows (newest first)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := a.client().Flows(cmd.Context(), f)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(list)
			}
			a.renderRows(list.Flows, true)
			switch {
			case list.Cursor != "":
				a.printf("%s\n", a.c(dim, fmt.Sprintf("%d of %d shown · next page: --cursor %s", len(list.Flows), list.Total, list.Cursor)))
			case list.Total > 0:
				a.printf("%s\n", a.c(dim, fmt.Sprintf("%d flows", list.Total)))
			default:
				a.printf("%s\n", a.c(dim, "no flows match"))
			}
			return nil
		},
	}
	addFilterFlags(cmd, &f)
	cmd.Flags().IntVarP(&f.Limit, "limit", "n", 50, "max rows (≤200)")
	cmd.Flags().StringVar(&f.Cursor, "cursor", "", "pagination cursor")
	return cmd
}

func (a *App) renderRows(rows []api.FlowRow, header bool) {
	if header && len(rows) > 0 {
		a.printf("%s\n", a.c(dim, pad("id", 7)+pad("time", 9)+pad("meth", 5)+pad("host", 28)+pad("path", 42)+pad("st", 4)+pad("dur", 8)+pad("up", 7)+pad("down", 7)+pad("type", 6)+"flags"))
	}
	for _, r := range rows {
		a.println(a.formatRow(r))
	}
}

func (a *App) formatRow(r api.FlowRow) string {
	st := "-"
	if r.Status > 0 {
		st = fmt.Sprintf("%d", r.Status)
	}
	if r.State == flow.StateActive {
		st = "…"
	}
	col := a.statusColor(r.Status, r.Error)
	meth := r.Method
	switch r.Kind {
	case flow.KindTunnel:
		meth = "TUN"
	case flow.KindWebSocket:
		meth = "WS"
	case flow.KindHTTP:
	}
	flags := strings.Join(r.Flags, ",")
	line := pad(r.Short, 7) + pad(r.Time.Local().Format("15:04:05"), 9) + pad(truncate(meth, 4), 5) +
		pad(truncate(r.Host, 27), 28) + pad(truncate(r.Path, 41), 42) +
		a.c(col, pad(st, 4)) + pad(r.Duration, 8) + pad(store.FormatBytes(r.Up), 7) + pad(store.FormatBytes(r.Down), 7) +
		pad(r.Type, 6) + a.c(dim, flags)
	if r.Error != "" {
		line += "\n" + strings.Repeat(" ", 16) + a.c(red, "↳ "+truncate(r.Error, 100))
	}
	if r.Type == "js" || r.Type == "css" || r.Type == "img" || r.Type == "font" {
		return a.c(dim, stripANSI(line))
	}
	return line
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '\033':
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (a *App) cmdTail() *cobra.Command {
	var f api.FlowFilter
	var all bool
	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Follow flows live (one line each; Ctrl-C to stop)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			c := a.client()
			if !c.Ping(ctx) {
				return fmt.Errorf("pano is not running (start with `pano start`)")
			}
			types := []string{"done", "held"}
			if all {
				types = nil
			}
			ch, err := c.Events(ctx, types, "")
			if err != nil {
				return err
			}
			matcher := store.Compile(f, time.Now())
			if !a.quiet && !a.jsonOut {
				a.printf("%s\n", a.c(dim, "waiting for traffic… (Ctrl-C to stop)"))
			}
			for ev := range ch {
				if ev.Type == flow.EvDropped {
					a.warn("%d events dropped", ev.Dropped)
					continue
				}
				if ev.Flow == nil || !matcher.Match(ev.Flow) {
					continue
				}
				if a.jsonOut {
					_ = a.printJSON(store.Row(ev.Flow))
					continue
				}
				row := store.Row(ev.Flow)
				if ev.Type == flow.EvHeld {
					a.printf("%s %s\n", a.c(magenta, "HELD "), a.formatRow(row))
					continue
				}
				a.println(a.formatRow(row))
			}
			return nil
		},
	}
	addFilterFlags(cmd, &f)
	cmd.Flags().BoolVar(&all, "all", false, "show every event (started, headers, done), not just completed flows")
	return cmd
}

func (a *App) cmdShow() *cobra.Command {
	var q api.FlowQuery
	var headers bool
	var out string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one flow (headers + body in a chosen view)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ok := flow.ParseShort(args[0])
			if !ok {
				return fmt.Errorf("bad flow id %q", args[0])
			}
			c := a.client()
			if out != "" {
				part := q.Part
				if part == "" || part == "both" {
					part = "response"
				}
				b, err := c.Body(cmd.Context(), id, part, true)
				if err != nil {
					return err
				}
				if err := os.WriteFile(out, b, 0o600); err != nil {
					return err
				}
				a.printf("%s wrote %d bytes to %s\n", a.c(green, "✓"), len(b), out)
				return nil
			}
			q.Headers = &headers
			d, err := c.Flow(cmd.Context(), id, q)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(d)
			}
			a.println(d.Text)
			return nil
		},
	}
	cmd.Flags().StringVar(&q.Part, "part", "both", "request|response|both")
	cmd.Flags().StringVarP(&q.View, "body", "b", "summary", "summary|schema|truncated|pretty|raw")
	cmd.Flags().StringVar(&q.Path, "path", "", "select a JSON value, e.g. choices.0.message.content")
	cmd.Flags().IntVar(&q.MaxBytes, "max-bytes", 0, "body budget (default 4096)")
	cmd.Flags().BoolVar(&headers, "headers", true, "include headers")
	cmd.Flags().BoolVar(&q.RevealSecrets, "reveal", false, "show secrets unredacted (audited)")
	cmd.Flags().StringVar(&out, "out", "", "write the decoded body to FILE instead")
	return cmd
}

func (a *App) cmdExplain() *cobra.Command {
	var req api.ExplainRequest
	cmd := &cobra.Command{
		Use:   "explain <id>",
		Short: "Digest an LLM API call: final message, tool calls, usage",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ok := flow.ParseShort(args[0])
			if !ok {
				return fmt.Errorf("bad flow id %q", args[0])
			}
			r, err := a.client().Explain(cmd.Context(), id, req)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(r)
			}
			a.println(r.Text)
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&req.Include, "include", nil, "final,usage,tools,stop,system,messages,thinking,errors,request")
	cmd.Flags().IntVar(&req.MaxChars, "max-chars", 0, "budget (default 4000)")
	cmd.Flags().StringVar(&req.Provider, "provider", "", "force provider: anthropic|openai-chat|openai-responses|gemini")
	return cmd
}

func (a *App) cmdDiff() *cobra.Command {
	var req api.DiffRequest
	cmd := &cobra.Command{
		Use:   "diff <a> <b>",
		Short: "Compare two flows (url, headers, status, JSON body structure)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var ok bool
			if req.A, ok = flow.ParseShort(args[0]); !ok {
				return fmt.Errorf("bad flow id %q", args[0])
			}
			if req.B, ok = flow.ParseShort(args[1]); !ok {
				return fmt.Errorf("bad flow id %q", args[1])
			}
			r, err := a.client().Diff(cmd.Context(), req)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(r)
			}
			a.println(r.Text)
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Part, "part", "response", "request|response|both")
	cmd.Flags().StringVar(&req.Path, "path", "", "restrict body diff to a JSON path")
	cmd.Flags().IntVar(&req.MaxChanges, "max-changes", 0, "cap (default 50)")
	cmd.Flags().StringSliceVar(&req.IgnoreHeaders, "ignore-headers", nil, "headers to ignore")
	return cmd
}

func (a *App) cmdReplay() *cobra.Command {
	var req api.ReplayRequest
	var hdrs []string
	var bodyArg string
	var sets []string
	var noRules bool
	cmd := &cobra.Command{
		Use:   "replay <id>",
		Short: "Re-send a captured request (with optional overrides)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ok := flow.ParseShort(args[0])
			if !ok {
				return fmt.Errorf("bad flow id %q", args[0])
			}
			for _, h := range hdrs {
				k, v, found := strings.Cut(h, ":")
				if !found {
					return fmt.Errorf("bad header %q (want Name: value)", h)
				}
				if req.SetHeaders == nil {
					req.SetHeaders = map[string]string{}
				}
				req.SetHeaders[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			if bodyArg != "" {
				b := bodyArg
				if strings.HasPrefix(bodyArg, "@") {
					data, err := os.ReadFile(bodyArg[1:])
					if err != nil {
						return err
					}
					b = string(data)
				}
				req.Body = &b
			}
			for _, s := range sets {
				k, v, found := strings.Cut(s, "=")
				if !found {
					return fmt.Errorf("bad --set %q (want path=value)", s)
				}
				if req.BodyPatch == nil {
					req.BodyPatch = map[string]any{}
				}
				req.BodyPatch[k] = v
			}
			if noRules {
				f := false
				req.FollowRules = &f
			}
			r, err := a.client().Replay(cmd.Context(), id, req)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(r)
			}
			col := a.statusColor(r.Status, r.Error)
			a.printf("%s replayed as %s → %s %s %s\n", a.c(green, "✓"), a.c(bold, r.Short), a.c(col, fmt.Sprint(r.Status)), r.Duration, store.FormatBytes(r.Size))
			if r.Error != "" {
				a.printf("  %s\n", a.c(red, r.Error))
			}
			if r.Summary != "" {
				a.println(r.Summary)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&req.URL, "url", "", "override URL")
	cmd.Flags().StringVarP(&req.Method, "method", "X", "", "override method")
	cmd.Flags().StringArrayVarP(&hdrs, "header", "H", nil, "set header (repeatable) 'Name: value'")
	cmd.Flags().StringSliceVar(&req.RemoveHeaders, "remove-header", nil, "remove headers")
	cmd.Flags().StringVar(&bodyArg, "body", "", "override body (or @file)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "patch JSON body: path=value (repeatable)")
	cmd.Flags().BoolVar(&noRules, "no-rules", false, "bypass live rules for this replay")
	cmd.Flags().IntVar(&req.TimeoutMS, "timeout", 0, "timeout ms (default 30000)")
	return cmd
}
