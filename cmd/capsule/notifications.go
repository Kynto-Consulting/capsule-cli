package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ── capsule notifications ────────────────────────────────────────────────────
// CLI.md 3.7. operationIds: notificationsList notificationsMark
// notifSettingsGet notifSettingsPut.

var notifChannels = map[string]bool{"in_app": true, "email": true, "slack_dm": true}

func init() {
	list := userLeaf("list", "List your notifications", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		unread, _ := cmd.Flags().GetBool("unread")
		p := "/api/v1/user/notifications"
		if unread {
			p += "?unread=true"
		}
		items, raw, err := fetchList(cmd, p)
		if err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		if quietIDs(cmd, items) {
			return nil
		}
		t := newTable(cmd, "ID", "", "EVENT", "PROJECT", "WHEN", "TITLE")
		for _, n := range items {
			mark := " "
			if n["read"] != true {
				mark = "*"
			}
			t.row(short8(sv(n, "id")), mark, sv(n, "event"), sv(n, "project"), age(sv(n, "created_at")), truncate(sv(n, "title"), 60))
		}
		return t.flush()
	})
	list.Flags().Bool("unread", false, "Only unread notifications")
	addList(list)

	read := userLeaf("read [ID...]", "Mark notifications as read (--all for every one)", cobra.ArbitraryArgs, func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		if all == (len(args) > 0) {
			return usageErr("give notification ids or --all, not both and not neither")
		}
		body := rec{}
		if all {
			body["all"] = true
		} else {
			var ids []string
			for _, a := range args {
				id, err := resolveShort("/api/v1/user/notifications", a, "notification")
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			body["ids"] = ids
		}
		if _, err := apiDo("PATCH", "/api/v1/user/notifications", body, nil, nil); err != nil {
			return err
		}
		fmt.Println("Marked as read.")
		return nil
	})
	read.Flags().Bool("all", false, "Mark every notification as read")

	sshow := userLeaf("show", "Show your notification settings", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		var s rec
		if _, err := apiDo("GET", "/api/v1/user/notification-settings", nil, &s, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, s); done || err != nil {
			return err
		}
		if ch, ok := s["channels"].(map[string]any); ok {
			for _, k := range sortedKeys(ch) {
				fmt.Printf("channel %-9s %s\n", k, map[bool]string{true: "on", false: "off"}[ch[k] == true])
			}
		}
		fmt.Printf("digest: %s\n", orDash(sv(s, "digest")))
		if ev, ok := s["events"].(map[string]any); ok {
			for _, k := range sortedKeys(ev) {
				fmt.Printf("event %-28s -> %s\n", k, orDash(fmtVal(ev[k])))
			}
		}
		return nil
	})
	sset := userLeaf("set", "Change your notification settings (only the given parts change)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		f := cmd.Flags()
		var cur rec
		if _, err := apiDo("GET", "/api/v1/user/notification-settings", nil, &cur, nil); err != nil {
			return err
		}
		changed := false
		if chs, _ := f.GetStringArray("channel"); len(chs) > 0 {
			m, _ := cur["channels"].(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			for _, kv := range chs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || !notifChannels[k] || (v != "on" && v != "off") {
					return usageErr("--channel is in_app|email|slack_dm=on|off; got %q", kv)
				}
				m[k] = v == "on"
			}
			cur["channels"] = m
			changed = true
		}
		if d, _ := f.GetString("digest"); d != "" {
			if d != "off" && d != "daily" && d != "weekly" {
				return usageErr("--digest must be off, daily or weekly")
			}
			cur["digest"] = d
			changed = true
		}
		if evs, _ := f.GetStringArray("event"); len(evs) > 0 {
			m, _ := cur["events"].(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			for _, kv := range evs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usageErr("--event is NAME=channel,channel (or NAME= to mute); got %q", kv)
				}
				chans := []string{}
				for _, c := range splitCSV(v) {
					if !notifChannels[c] {
						return usageErr("unknown channel %q (in_app, email, slack_dm)", c)
					}
					chans = append(chans, c)
				}
				m[k] = chans
			}
			cur["events"] = m
			changed = true
		}
		if !changed {
			return usageErr("nothing to change: pass --channel, --digest or --event")
		}
		body := rec{}
		for _, k := range []string{"channels", "digest", "events"} {
			if v, ok := cur[k]; ok {
				body[k] = v
			}
		}
		var out rec
		if _, err := apiDo("PUT", "/api/v1/user/notification-settings", body, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Println("Notification settings updated.")
		return nil
	})
	sset.Flags().StringArray("channel", nil, "in_app|email|slack_dm=on|off (repeatable)")
	sset.Flags().String("digest", "", "off, daily or weekly")
	sset.Flags().StringArray("event", nil, "NAME=channel,channel per event (repeatable)")
	settings := group("settings", "Notification channels, events and digest", sshow, sset)
	rootCmd.AddCommand(group("notifications", "Your notifications", list, read, settings))
}
