package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ── capsule webhooks ─────────────────────────────────────────────────────────
// CLI.md 3.7. operationIds: webhooksList webhookCreate webhookUpdate
// webhookDelete webhookTest webhookRotate deliveriesList deliveryRedeliver
// orgWebhooksList orgWebhookCreate. `--org-wide` selects the org-level hooks.

var webhookKinds = map[string]bool{"http": true, "slack": true, "discord": true, "email": true}

func hookBase(cmd *cobra.Command) (string, error) {
	if w, _ := cmd.Flags().GetBool("org-wide"); w {
		return op(cmd, "/webhooks")
	}
	return pp(cmd, "/webhooks")
}

func hookID(cmd *cobra.Command, ref string) (string, string, error) {
	base, err := hookBase(cmd)
	if err != nil {
		return "", "", err
	}
	id, err := resolveShort(base, ref, "webhook")
	return id, base, err
}

func hookBody(cmd *cobra.Command, creating bool) (rec, error) {
	f := cmd.Flags()
	b := rec{}
	if f.Changed("kind") {
		k, _ := f.GetString("kind")
		if !webhookKinds[k] {
			return nil, usageErr("--kind must be http, slack, discord or email")
		}
		b["kind"] = k
	}
	if f.Changed("url") {
		b["url"], _ = f.GetString("url")
	}
	if f.Changed("email-to") {
		b["email_to"] = csvFlag(cmd, "email-to")
	}
	if f.Changed("events") {
		b["events"] = csvFlag(cmd, "events")
	}
	if f.Changed("branch-filter") {
		b["branch_filter"], _ = f.GetString("branch-filter")
	}
	if f.Changed("active") {
		b["active"], _ = f.GetBool("active")
	}
	if creating {
		kind, _ := b["kind"].(string)
		if kind == "" {
			return nil, usageErr("--kind is required (http, slack, discord, email)")
		}
		if ev, _ := b["events"].([]string); len(ev) == 0 {
			return nil, usageErr("--events is required (comma separated event names)")
		}
		if kind == "email" {
			if et, _ := b["email_to"].([]string); len(et) == 0 {
				return nil, usageErr("--email-to is required for email webhooks")
			}
		} else if u, _ := b["url"].(string); u == "" {
			return nil, usageErr("--url is required for %s webhooks", kind)
		}
	}
	return b, nil
}

func addHookFlags(c *cobra.Command) {
	f := c.Flags()
	f.String("kind", "", "http, slack, discord or email")
	f.String("url", "", "Destination URL (not for email)")
	f.StringSlice("email-to", nil, "Recipients for email webhooks")
	f.StringSlice("events", nil, "Event names (comma separated)")
	f.String("branch-filter", "", "Only for events on matching branches (glob)")
	f.Bool("active", true, "Enabled")
}

func printSecretOnce(sec, what string) {
	if sec != "" {
		stderrf("%s signing secret (shown ONCE):\n", what)
		fmt.Println(sec)
	}
}

func init() {
	list := leaf("list", "List webhooks", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		base, err := hookBase(cmd)
		if err != nil {
			return err
		}
		var raw any
		if _, err := apiDo("GET", base, nil, &raw, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		items, _ := pageOf(raw)
		if quietIDs(cmd, items) {
			return nil
		}
		t := newTable(cmd, "ID", "KIND", "TARGET", "EVENTS", "ACTIVE", "LAST")
		for _, w := range items {
			t.row(short8(sv(w, "id")), sv(w, "kind"), truncate(sv(w, "url", "email_to"), 40), truncate(sv(w, "events"), 40), yn(w["active"] != false), sv(w, "last_delivery_status"))
		}
		return t.flush()
	})
	list.Flags().BoolP("quiet", "q", false, "Print only IDs")

	create := leaf("create", "Create a webhook (the signing secret is shown once)", cobra.NoArgs, func(cmd *cobra.Command, args []string) error {
		body, err := hookBody(cmd, true)
		if err != nil {
			return err
		}
		base, err := hookBase(cmd)
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", base, body, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Webhook %s created.\n", short8(sv(out, "id")))
		printSecretOnce(sv(out, "secret"), "Webhook")
		return nil
	})
	create.Example = `  capsule webhooks create --kind slack --url https://hooks.slack.com/services/... --events pr.merged,deploy.failed`
	addHookFlags(create)

	update := leaf("update ID", "Change a webhook (only the given flags change)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		body, err := hookBody(cmd, false)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return usageErr("nothing to change: pass at least one flag")
		}
		id, base, err := hookID(cmd, args[0])
		if err != nil {
			return err
		}
		// The contract's PATCH body requires kind and events: send the stored
		// values for everything the user did not change.
		var raw any
		if _, err := apiDo("GET", base, nil, &raw, nil); err != nil {
			return err
		}
		items, _ := pageOf(raw)
		for _, w := range items {
			if sv(w, "id") != id {
				continue
			}
			for _, k := range []string{"kind", "url", "email_to", "events", "active", "branch_filter"} {
				if _, set := body[k]; !set {
					if v, ok := w[k]; ok && v != nil {
						body[k] = v
					}
				}
			}
		}
		var out rec
		if _, err := apiDo("PATCH", base+"/"+seg(id), body, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Webhook %s updated.\n", short8(id))
		return nil
	})
	addHookFlags(update)
	update.Flags().Lookup("active").DefValue = "true"

	del := leaf("delete ID", "Delete a webhook", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, base, err := hookID(cmd, args[0])
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Delete webhook "+short8(id)+"?"); err != nil {
			return err
		}
		if _, err := apiDo("DELETE", base+"/"+seg(id), nil, nil, nil); err != nil {
			return err
		}
		fmt.Printf("Webhook %s deleted.\n", short8(id))
		return nil
	})
	addYes(del)

	test := leaf("test ID", "Send a test event", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, base, err := hookID(cmd, args[0])
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", base+"/"+seg(id)+"/test", rec{}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Test event sent to webhook %s.\n", short8(id))
		return nil
	})

	rotate := leaf("rotate-secret ID", "Generate a new signing secret (shown once)", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, base, err := hookID(cmd, args[0])
		if err != nil {
			return err
		}
		if err := confirmOr(cmd, "Rotate the signing secret of webhook "+short8(id)+"? The old one stops working."); err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", base+"/"+seg(id)+"/rotate-secret", rec{}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		printSecretOnce(sv(out, "secret"), "New")
		return nil
	})
	addYes(rotate)

	deliveries := leaf("deliveries ID", "List the recent deliveries of a webhook", cobra.ExactArgs(1), func(cmd *cobra.Command, args []string) error {
		id, base, err := hookID(cmd, args[0])
		if err != nil {
			return err
		}
		items, raw, err := fetchList(cmd, base+"/"+seg(id)+"/deliveries")
		if err != nil {
			return err
		}
		if done, err := emit(cmd, raw); done || err != nil {
			return err
		}
		if quietIDs(cmd, items) {
			return nil
		}
		t := newTable(cmd, "ID", "EVENT", "STATUS", "MS", "ATTEMPT", "WHEN", "ERROR")
		for _, d := range items {
			t.row(short8(sv(d, "id")), sv(d, "event"), sv(d, "status_code"), sv(d, "duration_ms"), sv(d, "attempt"), age(sv(d, "delivered_at")), truncate(sv(d, "error"), 40))
		}
		return t.flush()
	})
	addList(deliveries)

	redeliver := leaf("redeliver WEBHOOK_ID DELIVERY_ID", "Send a past delivery again", cobra.ExactArgs(2), func(cmd *cobra.Command, args []string) error {
		id, base, err := hookID(cmd, args[0])
		if err != nil {
			return err
		}
		did, err := resolveShort(base+"/"+seg(id)+"/deliveries", args[1], "delivery")
		if err != nil {
			return err
		}
		var out rec
		if _, err := apiDo("POST", base+"/"+seg(id)+"/deliveries/"+seg(did)+"/redeliver", rec{}, &out, nil); err != nil {
			return err
		}
		if done, err := emit(cmd, out); done || err != nil {
			return err
		}
		fmt.Printf("Delivery %s sent again.\n", short8(did))
		return nil
	})

	for _, c := range []*cobra.Command{list, create, update, del, test, rotate, deliveries, redeliver} {
		c.Flags().Bool("org-wide", false, "Use organization-level webhooks instead of the project's")
	}
	rootCmd.AddCommand(group("webhooks", "Outgoing webhooks for product events", list, create, update, del, test, rotate, deliveries, redeliver))
}
