package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/appnice/mailroom/internal/api"
	"github.com/appnice/mailroom/internal/envelope"
)

func cmdSend(use, kind string) *cobra.Command {
	var to, intent, subject, body, priority, data, dedupe, deadline, saveDir string
	var attach []string
	var wait int
	short := map[string]string{"tell": "Send a tell (no reply expected)", "ask": "Ask an agent; prints a ticket or, with --wait, the reply", "event": "Emit an event (dedupable machine signal)"}[kind]
	c := &cobra.Command{
		Use:   use + " --to <agent> --intent <a.b> --subject <text> [--body <text>|-]",
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if body == "-" {
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				body = string(b)
			}
			cl, err := senderClient()
			if err != nil {
				return notRunning(err)
			}
			defer cl.Close()
			d := envelope.Draft{To: to, Kind: kind, Intent: intent, Subject: subject, Body: body, Priority: priority, DedupeKey: dedupe}
			if data != "" {
				d.Data = json.RawMessage(data)
			}
			if deadline != "" {
				dur, err := time.ParseDuration(deadline)
				if err != nil {
					t, err2 := time.Parse(time.RFC3339, deadline)
					if err2 != nil {
						return fmt.Errorf("--deadline: use a duration (2h) or RFC 3339 time")
					}
					d.Deadline = &t
				} else {
					t := time.Now().Add(dur).UTC()
					d.Deadline = &t
				}
			}
			for _, f := range attach {
				b, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				var att envelope.Attachment
				if err := cl.Call("blob.put", api.BlobPutParams{Name: envelope.SanitizeName(filepath.Base(f)), Data: b}, &att, 60*time.Second); err != nil {
					return fmt.Errorf("upload %s: %w", f, err)
				}
				d.Attachments = append(d.Attachments, att)
			}
			method := map[string]string{"tell": "message.send", "ask": "message.ask", "event": "message.emit"}[kind]
			var res api.SendResult
			if err := cl.Call(method, api.SendParams{Draft: d, WaitS: wait}, &res, time.Duration(wait+15)*time.Second); err != nil {
				return err
			}
			if flagJSON {
				printJSON(res)
				return nil
			}
			switch {
			case res.Coalesced:
				fmt.Printf("coalesced into %s (state %s)\n", res.CoalescedInto, res.State)
			case res.Reply != nil:
				printReply(res.Reply)
				return saveAttachments(cl, res.Reply, saveDir)
			case kind == "ask":
				fmt.Printf("ticket %s (state %s); wait with `mailroom await %s`\n", res.ID, res.State, res.ID)
			default:
				fmt.Printf("%s %s (state %s, thread %s)\n", kind, res.ID, res.State, res.ThreadID)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&to, "to", "", "recipient agent id")
	f.StringVar(&intent, "intent", "", "dotted intent, e.g. store.status")
	f.StringVar(&subject, "subject", "", "one-line subject")
	f.StringVar(&body, "body", "", "body text, or - for stdin")
	f.StringVar(&priority, "priority", "normal", "low, normal, high or urgent")
	f.StringVar(&data, "data", "", "JSON object passed to the handler")
	f.StringArrayVar(&attach, "attach", nil, "file to attach (repeatable)")
	f.StringVar(&deadline, "deadline", "", "duration (2h) or RFC 3339 time")
	f.StringVar(&flagAs, "as", "", "send as this agent (default: $MAILROOM_AGENT, else the human)")
	if kind == "event" {
		f.StringVar(&dedupe, "dedupe-key", "", "coalesce repeats with the same key")
	}
	if kind == "ask" {
		f.IntVar(&wait, "wait", 0, "seconds to wait for the reply (max 600)")
		f.StringVar(&saveDir, "save", "", "directory to save reply attachments into")
	}
	_ = c.MarkFlagRequired("to")
	_ = c.MarkFlagRequired("intent")
	_ = c.MarkFlagRequired("subject")
	return c
}

func printReply(r *api.ReplyView) {
	fmt.Printf("reply %s from %s (status %s)\n%s\n", r.ID, r.From, r.Status, r.Text)
	if len(r.Data) > 0 {
		fmt.Printf("data: %s\n", string(r.Data))
	}
	for _, a := range r.Attachments {
		fmt.Printf("attachment: %s (%d bytes, %s) sha256 %s\n", a.Name, a.Size, a.MIME, a.SHA256)
	}
}

func saveAttachments(cl *api.Client, r *api.ReplyView, dir string) error {
	if dir == "" || len(r.Attachments) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, a := range r.Attachments {
		var b api.BlobGetResult
		if err := cl.Call("blob.get", api.IDParams{ID: a.SHA256}, &b, 60*time.Second); err != nil {
			return err
		}
		p := filepath.Join(dir, envelope.SanitizeName(a.Name))
		if err := os.WriteFile(p, b.Data, 0o644); err != nil {
			return err
		}
		fmt.Println("saved", p)
	}
	return nil
}

func cmdAwait() *cobra.Command {
	var wait int
	var saveDir string
	c := &cobra.Command{
		Use:   "await <ticket>",
		Short: "Wait for the reply to one of your asks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := senderClient()
			if err != nil {
				return notRunning(err)
			}
			defer cl.Close()
			var res api.AwaitResult
			if err := cl.Call("message.await", api.AwaitParams{Ticket: args[0], WaitS: wait}, &res, time.Duration(wait+15)*time.Second); err != nil {
				return err
			}
			if flagJSON {
				printJSON(res)
				return nil
			}
			if res.Reply == nil {
				fmt.Printf("no reply yet (ask is %s)\n", res.State)
				return nil
			}
			printReply(res.Reply)
			return saveAttachments(cl, res.Reply, saveDir)
		},
	}
	c.Flags().IntVar(&wait, "wait", 30, "seconds to wait (max 600)")
	c.Flags().StringVar(&saveDir, "save", "", "directory to save reply attachments into")
	c.Flags().StringVar(&flagAs, "as", "", "the agent that asked")
	return c
}

func cmdReply() *cobra.Command {
	return &cobra.Command{
		Use:   "reply <message> <text>",
		Short: "Answer a needs_human item; the thread continues",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res api.SendResult
			if err := call("message.reply", api.HumanReplyParams{MessageID: args[0], Text: args[1]}, &res, 10*time.Second); err != nil {
				return notRunning(err)
			}
			if flagJSON {
				printJSON(res)
				return nil
			}
			fmt.Printf("answered; continuation %s (state %s, thread %s)\n", res.ID, res.State, res.ThreadID)
			return nil
		},
	}
}

func cmdRetry() *cobra.Command {
	return &cobra.Command{
		Use: "retry <message>", Short: "Requeue a dead letter", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := call("message.retry", api.IDParams{ID: args[0]}, nil, 10*time.Second); err != nil {
				return notRunning(err)
			}
			fmt.Println("requeued", args[0])
			return nil
		},
	}
}

func cmdCancel() *cobra.Command {
	return &cobra.Command{
		Use: "cancel <message>", Short: "Cancel a queued or running item", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r api.CancelResult
			if err := call("message.cancel", api.IDParams{ID: args[0]}, &r, 10*time.Second); err != nil {
				return notRunning(err)
			}
			fmt.Println(cancelLine(args[0], r.State))
			return nil
		},
	}
}

// cancelLine tells the state message.cancel returns: rejected at once, or
// a running worker that is stopping.
func cancelLine(id, state string) string {
	switch state {
	case api.CancelRejected:
		return "cancelled " + id + ": rejected"
	case api.CancelCancelling:
		return "cancelling " + id + ": the worker is stopping; see mailroom thread " + id
	}
	return "cancel sent for " + id + " (state " + state + ")"
}

func cmdBlob() *cobra.Command {
	c := &cobra.Command{Use: "blob", Short: "Attachment storage"}
	var out string
	get := &cobra.Command{
		Use: "get <sha256>", Short: "Fetch an attachment", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := senderClient()
			if err != nil {
				return notRunning(err)
			}
			defer cl.Close()
			var b api.BlobGetResult
			if err := cl.Call("blob.get", api.IDParams{ID: args[0]}, &b, 60*time.Second); err != nil {
				return err
			}
			if out == "" {
				_, err := os.Stdout.Write(b.Data)
				return err
			}
			return os.WriteFile(out, b.Data, 0o644)
		},
	}
	get.Flags().StringVarP(&out, "output", "o", "", "write to file instead of stdout")
	get.Flags().StringVar(&flagAs, "as", "", "fetch as this agent")
	c.AddCommand(get)
	return c
}
