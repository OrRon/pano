package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func (a *App) cmdRules() *cobra.Command {
	cmd := &cobra.Command{Use: "rules", Short: "Live traffic rules: delay, mock, block, rewrite, throttle, breakpoints"}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List rules",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rules, err := a.client().Rules(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(rules)
			}
			if len(rules) == 0 {
				a.printf("%s\n", a.c(dim, "no rules"))
				return nil
			}
			for _, r := range rules {
				a.println(a.formatRule(r))
			}
			return nil
		},
	}
	var preset, file, name string
	var ttl int
	var params []string
	var matchHost, matchPath, matchStatus string
	var methods []string
	var actions []string
	add := &cobra.Command{
		Use:   "add",
		Short: "Add a rule from a preset, a JSON file, or flags",
		Example: `  pano rules add --preset slow_network --param host=api.openai.com --param ms=3000 --ttl 600
  pano rules add --preset fail_rate --param host=api.anthropic.com --param rate=0.5
  pano rules add --host api.openai.com --action delay:2000 --action set_header:X-Debug=1
  pano rules add --file rule.json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := api.RuleAddRequest{Name: name, TTLS: ttl}
			switch {
			case preset != "":
				req.Preset = preset
				req.Params = map[string]any{}
				for _, p := range params {
					k, v, ok := strings.Cut(p, "=")
					if !ok {
						return fmt.Errorf("bad --param %q (want key=value)", p)
					}
					req.Params[k] = coerce(v)
				}
			case file != "":
				b, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				var r api.Rule
				if err := json.Unmarshal(b, &r); err != nil {
					return fmt.Errorf("parse %s: %w", file, err)
				}
				req.Rule = &r
			default:
				if len(actions) == 0 {
					return fmt.Errorf("need --preset, --file, or --action")
				}
				r := api.Rule{Match: api.Match{Host: matchHost, Path: matchPath, Status: matchStatus, Method: methods}}
				for _, s := range actions {
					act, err := parseAction(s)
					if err != nil {
						return err
					}
					r.Actions = append(r.Actions, act)
				}
				req.Rule = &r
			}
			rule, err := a.client().AddRule(cmd.Context(), req)
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(rule)
			}
			a.printf("%s added %s\n", a.c(green, "✓"), a.formatRule(rule))
			return nil
		},
	}
	add.Flags().StringVar(&preset, "preset", "", "slow_network|fail_rate|offline_host|timeout|rate_limit|hold")
	add.Flags().StringArrayVar(&params, "param", nil, "preset parameter key=value (repeatable)")
	add.Flags().StringVar(&file, "file", "", "rule JSON file")
	add.Flags().StringVar(&name, "name", "", "rule name")
	add.Flags().IntVar(&ttl, "ttl", 0, "auto-remove after N seconds")
	add.Flags().StringVar(&matchHost, "host", "", "match host glob")
	add.Flags().StringVar(&matchPath, "path", "", "match path (glob or /regex/)")
	add.Flags().StringVar(&matchStatus, "status", "", "match response status (4xx, 500, !2xx)")
	add.Flags().StringSliceVar(&methods, "method", nil, "match methods")
	add.Flags().StringArrayVar(&actions, "action", nil, "action: delay:MS | set_header:Name=v | remove_header:Name | mock:STATUS[:body] | block[:reset|timeout] | redirect:URL | throttle:KBPS | breakpoint[:request|response] | tag:name")

	rm := &cobra.Command{
		Use:   "rm <id>|--all",
		Short: "Remove a rule",
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			if all {
				n, err := a.client().RemoveAllRules(cmd.Context())
				if err != nil {
					return err
				}
				a.printf("%s removed %d rules\n", a.c(green, "✓"), n)
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("rule id required (or --all)")
			}
			if err := a.client().RemoveRule(cmd.Context(), args[0]); err != nil {
				return err
			}
			a.printf("%s removed %s\n", a.c(green, "✓"), args[0])
			return nil
		},
	}
	rm.Flags().Bool("all", false, "remove every rule")
	toggle := &cobra.Command{
		Use:   "toggle <id>",
		Short: "Enable or disable a rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			on, _ := cmd.Flags().GetBool("on")
			off, _ := cmd.Flags().GetBool("off")
			var en *bool
			switch {
			case on:
				t := true
				en = &t
			case off:
				f := false
				en = &f
			default:
				cur, err := a.client().Rules(cmd.Context())
				if err != nil {
					return err
				}
				for _, r := range cur {
					if r.ID == args[0] {
						v := r.Enabled == nil || !*r.Enabled
						en = &v
					}
				}
				if en == nil {
					return fmt.Errorf("rule %s not found", args[0])
				}
			}
			r, err := a.client().UpdateRule(cmd.Context(), args[0], api.RulePatch{Enabled: en})
			if err != nil {
				return err
			}
			a.printf("%s %s\n", a.c(green, "✓"), a.formatRule(r))
			return nil
		},
	}
	toggle.Flags().Bool("on", false, "enable")
	toggle.Flags().Bool("off", false, "disable")
	presets := &cobra.Command{
		Use:   "presets",
		Short: "List presets and their parameters",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ps, err := a.client().Presets(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(ps)
			}
			for _, p := range ps {
				var kv []string
				for k, v := range p.Params {
					kv = append(kv, fmt.Sprintf("%s=%v", k, v))
				}
				a.printf("%s  %s\n    %s\n", a.c(bold, pad(p.Name, 14)), p.Description, a.c(dim, strings.Join(kv, " ")))
			}
			return nil
		},
	}
	cmd.AddCommand(ls, add, rm, toggle, presets)
	return cmd
}

func coerce(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	if v == "true" || v == "false" {
		return v == "true"
	}
	return v
}

func parseAction(s string) (api.Action, error) {
	typ, rest, _ := strings.Cut(s, ":")
	act := api.Action{Type: typ}
	switch typ {
	case "delay":
		n, err := strconv.Atoi(rest)
		if err != nil {
			return act, fmt.Errorf("delay: want delay:MS")
		}
		act.MS = n
	case "set_header":
		k, v, ok := strings.Cut(rest, "=")
		if !ok {
			return act, fmt.Errorf("set_header: want set_header:Name=value")
		}
		act.Name, act.Value = k, v
	case "remove_header", "tag":
		if typ == "tag" {
			act.Tags = []string{rest}
		} else {
			act.Name = rest
		}
	case "mock":
		st, body, _ := strings.Cut(rest, ":")
		n, err := strconv.Atoi(st)
		if err != nil {
			return act, fmt.Errorf("mock: want mock:STATUS[:body]")
		}
		act.Status, act.Body = n, body
	case "block":
		act.Mode = rest
	case "redirect":
		act.Upstream = rest
	case "throttle":
		n, err := strconv.Atoi(rest)
		if err != nil {
			return act, fmt.Errorf("throttle: want throttle:KBPS")
		}
		act.KBps = n
	case "breakpoint", "hold":
		act.Type = "breakpoint"
		act.On = rest
	default:
		return act, fmt.Errorf("unknown action %q", typ)
	}
	return act, nil
}

func (a *App) formatRule(r api.Rule) string {
	en := a.c(green, "●")
	if r.Enabled != nil && !*r.Enabled {
		en = a.c(dim, "○")
	}
	var m []string
	if r.Match.Host != "" {
		m = append(m, "host="+r.Match.Host)
	}
	if r.Match.Path != "" {
		m = append(m, "path="+r.Match.Path)
	}
	if len(r.Match.Method) > 0 {
		m = append(m, "method="+strings.Join(r.Match.Method, "|"))
	}
	if r.Match.Status != "" {
		m = append(m, "status="+r.Match.Status)
	}
	if len(m) == 0 {
		m = append(m, "*")
	}
	var acts []string
	for _, x := range r.Actions {
		acts = append(acts, x.Type)
	}
	extra := ""
	if r.Probability > 0 && r.Probability < 1 {
		extra += fmt.Sprintf(" p=%.2f", r.Probability)
	}
	if r.TTLSeconds > 0 {
		extra += fmt.Sprintf(" ttl=%ds", r.TTLSeconds)
	}
	name := r.Name
	if name != "" {
		name = " " + a.c(bold, name)
	}
	return fmt.Sprintf("%s %s%s  %s → %s%s  %s", en, r.ID, name, strings.Join(m, " "), strings.Join(acts, ","), extra, a.c(dim, fmt.Sprintf("hits=%d", r.Hits)))
}

func (a *App) cmdBP() *cobra.Command {
	cmd := &cobra.Command{Use: "bp", Short: "Breakpoints: inspect and release held requests"}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List held requests",
		RunE: func(cmd *cobra.Command, _ []string) error {
			held, err := a.client().Held(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				return a.printJSON(held)
			}
			if len(held) == 0 {
				a.printf("%s\n", a.c(dim, "nothing held"))
				return nil
			}
			for _, h := range held {
				a.printf("%s %s  %s %s  %s  age %s  rule %s\n", a.c(magenta, "HELD"), a.c(bold, h.Short), h.Phase, h.Method, h.URL, h.Age, h.RuleID)
			}
			return nil
		},
	}
	var hdrs []string
	var bodyArg string
	var status int
	resume := &cobra.Command{
		Use:   "resume <id>",
		Short: "Release a held request/response, optionally edited",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ok := flow.ParseShort(args[0])
			if !ok {
				return fmt.Errorf("bad flow id %q", args[0])
			}
			req := api.ResumeRequest{Action: "resume", Status: status}
			for _, h := range hdrs {
				k, v, found := strings.Cut(h, ":")
				if !found {
					return fmt.Errorf("bad header %q", h)
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
			if err := a.client().Resume(cmd.Context(), id, req); err != nil {
				return err
			}
			a.printf("%s resumed %s\n", a.c(green, "✓"), id.Short())
			return nil
		},
	}
	resume.Flags().StringArrayVarP(&hdrs, "header", "H", nil, "set header 'Name: value'")
	resume.Flags().StringVar(&bodyArg, "body", "", "replace body (or @file)")
	resume.Flags().IntVar(&status, "status", 0, "override status (response phase)")
	drop := &cobra.Command{
		Use:   "drop <id>",
		Short: "Drop a held request (client sees a reset)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, ok := flow.ParseShort(args[0])
			if !ok {
				return fmt.Errorf("bad flow id %q", args[0])
			}
			if err := a.client().Resume(cmd.Context(), id, api.ResumeRequest{Action: "drop"}); err != nil {
				return err
			}
			a.printf("%s dropped %s\n", a.c(green, "✓"), id.Short())
			return nil
		},
	}
	cmd.AddCommand(ls, resume, drop)
	return cmd
}
