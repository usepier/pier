package api

import (
	"os"
	"path/filepath"
	"time"

	"github.com/usepier/pier/internal/config"
	"github.com/usepier/pier/pkg/pier"
	"github.com/usepier/pier/pkg/pier/wire"
)

func wireSession(be Backend, s pier.Session) wire.Session {
	w := wire.Session{
		ID: s.ID, Name: s.Name, Repo: s.Repo, Origin: s.Origin, Base: s.Base, Branch: s.Branch,
		State: wire.SessionState(s.State), Label: pier.Label(s), Activity: pier.Activity(s),
		Attached: s.Attached, Strained: s.Strained, Setup: s.Setup,
		MachineType: s.InstanceType, CostNote: s.CostNote, HourlyUSD: pier.HourlyUSD(s),
		CreatedAt: wire.TimeOrNil(s.Created), Pooled: s.PoolGen != "",
		HasKey:     s.State == pier.StateFailed || be.HasKey(s),
		FailReason: s.FailReason, LogPath: s.LogPath,
		ParkAt: wire.TimeOrNil(s.ParkAt), CapAt: wire.TimeOrNil(s.CapAt),
	}
	if r := s.Res; r != nil {
		w.Resources = &wire.Resources{CPU: r.CPU, CPUs: r.CPUs, MemUsed: r.MemUsed, MemTotal: r.MemTotal,
			DiskUsed: r.DiskUsed, DiskTotal: r.DiskTotal, Load1: r.Load1}
	}
	for _, p := range s.Ports {
		w.Ports = append(w.Ports, wire.Port{Port: p.Port, Process: p.Process, PID: p.PID})
	}
	return w
}

func wireCheckout(co pier.Checkout) wire.Checkout {
	return wire.Checkout{Path: co.Path, Name: co.Name, Origin: co.Origin, Missing: co.Missing}
}

func wireRepo(r pier.Repo, co *wire.Checkout, origin string) wire.Repo {
	out := wire.Repo{
		Name: r.Name, Origin: origin, Checkout: co, Image: r.Image,
		Baking:     pier.BakeRunning(r.Name),
		PoolTarget: r.PoolSize, PoolReady: r.Ready, PoolFilling: r.Filling, PoolStale: r.Stale,
		PoolAges: append([]string{}, r.PoolAges...), Sessions: r.Sessions, MonthlyUSD: r.MonthlyUSD,
		Reminders: []wire.Reminder{},
	}
	switch {
	case r.Image == "":
		out.ImageKind = wire.ImageNone
	case r.Baked == nil:
		out.ImageKind = wire.ImageUnknown
	case r.Baked.RepoIncluded:
		out.ImageKind = wire.ImagePrebuilt
	default:
		out.ImageKind = wire.ImageToolchain
	}
	if r.Baked != nil {
		out.BakedAt = wire.TimeOrNil(r.Baked.BakedAt)
	}
	if co != nil && !co.Missing {
		if st, err := os.Stat(filepath.Join(co.Path, ".pier")); err == nil && st.IsDir() {
			out.HasPier = true
		}
	}
	for _, m := range r.Reminders {
		out.Reminders = append(out.Reminders, wire.Reminder{Message: m.Message, Action: m.Action})
	}
	return out
}

// buildOverview assembles the wire overview from one listing.
func buildOverview(be Backend, all []pier.Session, quota *wire.Quota, enriched bool) wire.Overview {
	checkouts := be.Checkouts()
	var roots []string
	byName := map[string]*wire.Checkout{}
	wc := make([]wire.Checkout, 0, len(checkouts))
	for _, co := range checkouts {
		wc = append(wc, wireCheckout(co))
	}
	for i := range wc {
		if wc[i].Missing {
			continue
		}
		if _, ok := byName[wc[i].Name]; !ok {
			byName[wc[i].Name] = &wc[i]
			roots = append(roots, wc[i].Path)
		}
	}
	o := wire.Overview{
		Cloud: be.Cloud(), Sessions: make([]wire.Session, 0, len(all)), Checkouts: wc,
		Quota: quota, UpdatedAt: time.Now().UTC(), Enriched: enriched,
		Repos: []wire.Repo{},
	}
	origins := map[string]string{}
	disk := be.DiskMonthlyUSD()
	hourly, parked := map[string]float64{}, map[string]int{}
	for _, s := range all {
		w := wireSession(be, s)
		o.Sessions = append(o.Sessions, w)
		if s.Origin != "" && origins[s.Repo] == "" {
			origins[s.Repo] = s.Origin
		}
		hourly[s.Repo] += w.HourlyUSD
		if s.State == pier.StateParked && !w.Pooled {
			parked[s.Repo]++
		}
		if w.Pooled {
			if s.State == pier.StateParked {
				o.Totals.PoolReady++
			}
			o.Totals.HourlyUSD += w.HourlyUSD
			continue
		}
		switch s.State {
		case pier.StateCreating, pier.StateRunning, pier.StateWorking, pier.StateIdle:
			o.Totals.Running++
			o.Totals.HourlyUSD += w.HourlyUSD
		case pier.StateParked:
			o.Totals.Parked++
			o.Totals.IdleMonthlyUSD += disk
		}
	}
	for _, r := range be.ReposFor(all, roots) {
		co := byName[r.Name]
		origin := origins[r.Name]
		if co != nil && co.Origin != "" {
			origin = co.Origin
		}
		wr := wireRepo(r, co, origin)
		wr.HourlyUSD = hourly[r.Name]
		wr.IdleMonthlyUSD = r.MonthlyUSD + float64(parked[r.Name])*disk
		o.Repos = append(o.Repos, wr)
		o.Totals.IdleMonthlyUSD += r.MonthlyUSD
	}
	return o
}

func wireSettings(be Backend) wire.Settings {
	out := wire.Settings{Settings: []wire.Setting{}}
	for _, g := range config.Groups {
		out.Groups = append(out.Groups, wire.SettingGroup{Key: g.Key, Title: g.Title})
	}
	for _, v := range be.Settings() {
		st := wire.Setting{
			Key: v.Key, Group: v.Group, Label: v.Label, Hint: v.Hint, Detail: v.Detail,
			Default: v.Default, NoCustom: v.NoCustom, Empty: v.Empty, Suffix: v.Suffix,
			Value: v.Value, Display: v.Display,
		}
		switch v.Kind {
		case config.KindChoice:
			st.Kind = wire.SettingChoice
		case config.KindMachine:
			st.Kind = wire.SettingMachine
		default:
			st.Kind = wire.SettingText
		}
		for _, o := range v.Options {
			st.Options = append(st.Options, wire.SettingOption{Value: o.Value, Label: o.Label, Desc: o.Desc})
		}
		out.Settings = append(out.Settings, st)
	}
	return out
}

func wireMachines(ms []pier.Machine) []wire.Machine {
	out := make([]wire.Machine, 0, len(ms))
	for _, m := range ms {
		out = append(out, wire.Machine{Type: m.Type, CPU: m.CPU, Mem: m.Mem, Cost: m.Cost})
	}
	return out
}

func wireEvent(e pier.Event) wire.Event {
	return wire.Event{Kind: wire.EventKind(e.Kind), Message: e.Message, ElapsedMs: e.Elapsed.Milliseconds()}
}
