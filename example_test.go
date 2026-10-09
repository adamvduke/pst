package pst_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/adamvduke/pst"
)

// show prints escape sequences readably.
func show(b []byte) {
	s := strings.NewReplacer("\x1b", `\e`, "\a", `\a`).Replace(string(b))
	for _, line := range strings.SplitAfter(s, `\e\`) {
		if line != "" {
			fmt.Println(line)
		}
	}
}

func ExampleStatus() {
	// Real programs use pst.NewForFile(os.Stdout, ...), which does nothing
	// unless stdout is a terminal.
	var out bytes.Buffer
	st := pst.New(&out, pst.WithApp("brew"))

	st.Working("Installing updates")
	st.Blocked(pst.KindAuth, "Password required")
	st.Progress(60, "Installing updates")
	st.Done("Upgraded 12 packages")

	show(out.Bytes())
	// Output:
	// \e]7501;state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\e\
	// \e]7501;state=blocked:kind=auth:app=brew:msg=UGFzc3dvcmQgcmVxdWlyZWQ=\e\
	// \e]7501;state=working:progress=60:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\e\
	// \e]7501;state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=\e\
}

func ExampleStatus_Child() {
	var out bytes.Buffer
	root := pst.New(&out, pst.WithApp("deploy"))
	root.Working("Deploying v2.4.1")

	// Children take app=deploy from the root on the terminal side.
	east, _ := root.Child("us-east", pst.WithTitle("US East"))
	east.Progress(40, "Pushing image")
	east.Done("Healthy")
	east.Clear()

	root.Done("Deployed")
	show(out.Bytes())
	// Output:
	// \e]7501;state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==\e\
	// \e]7501;state=working:id=us-east:progress=40:title=VVMgRWFzdA==:msg=UHVzaGluZyBpbWFnZQ==\e\
	// \e]7501;state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==\e\
	// \e]7501;state=clear:id=us-east\e\
	// \e]7501;state=done:app=deploy:msg=RGVwbG95ZWQ=\e\
}

// A program that exits when it finishes reports done or error just before
// exiting, and idle when the user interrupts it.
func Example_lifecycle() {
	var out bytes.Buffer
	st := pst.New(&out, pst.WithApp("sync")) // pst.NewForFile(os.Stdout, ...) in real code

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := func() (err error) {
		defer func() {
			switch {
			case errors.Is(err, context.Canceled):
				st.Idle("Interrupted")
			case err != nil:
				st.Errorf("Sync failed: %v", err)
			default:
				st.Done("Photos synced")
			}
		}()
		st.Working("Syncing photos")
		return syncPhotos(ctx)
	}()
	_ = err

	show(out.Bytes())
	// Output:
	// \e]7501;state=working:app=sync:msg=U3luY2luZyBwaG90b3M=\e\
	// \e]7501;state=done:app=sync:msg=UGhvdG9zIHN5bmNlZA==\e\
}

func syncPhotos(ctx context.Context) error { return ctx.Err() }

func ExampleDetect() {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	switch s, err := pst.Detect(ctx, nil); {
	case err != nil:
		fmt.Println("no terminal:", err)
	case s == pst.Supported:
		fmt.Println("terminal supports OSC 7501")
	default:
		// Unsupported or Unknown. Sending reports anyway is safe.
		fmt.Println(s)
	}
}

func ExampleParseBody() {
	// From an emulator's OSC dispatch, with the "7501;" prefix removed.
	body := []byte("state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==")
	r, err := pst.ParseBody(body)
	if errors.Is(err, pst.ErrQuery) {
		// Reply to feature detection: os.Stdout.Write(pst.Query())
		return
	}
	if err != nil {
		return // discarded or ignored; nothing to apply
	}
	p, _ := r.Progress.Value()
	fmt.Println(r.State, r.ID, r.Title, p, r.Msg)
	// Output: working us-east US East 40 Pushing image
}

func ExampleStore() {
	store := pst.NewStore()
	for _, body := range []string{
		"state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==",
		"state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==",
		"state=working:id=us-east:progress=40",
	} {
		r, err := pst.ParseBody([]byte(body))
		if err != nil {
			continue
		}
		for _, c := range store.Apply(r) {
			fmt.Printf("%s %q\n", c.Kind, c.ID)
		}
	}
	for _, rec := range store.All() {
		fmt.Printf("%q %s app=%s\n", rec.ID, rec.State, store.EffectiveApp(rec.ID))
	}

	// A new shell prompt (OSC 133 A) drops working and blocked records.
	fmt.Println(len(store.PromptStarted()), "removed;", store.Len(), "left")
	// Output:
	// added ""
	// added "eu-west"
	// added "us-east"
	// "" working app=deploy
	// "eu-west" blocked app=deploy
	// "us-east" working app=deploy
	// 3 removed; 0 left
}

func ExampleScanner() {
	ptyOutput := []byte("building...\r\n\x1b]7501;state=working:app=make:msg=QnVpbGRpbmc=\x1b\\\x1b]0;title\adone\r\n")

	var sc pst.Scanner
	store := pst.NewStore()
	sc.Feed(ptyOutput, func(seq []byte) {
		r, err := pst.ParseSequence(seq)
		switch {
		case errors.Is(err, pst.ErrQuery):
			// write pst.Query() back to the pty
		case err == nil:
			store.Apply(r)
		}
	})
	rec, _ := store.Get("")
	fmt.Println(rec.State, rec.App, rec.Msg)
	// Output: working make Building
}

func ExampleReport_Encode() {
	seq, err := pst.Report{State: pst.StateWorking, App: "brew", Msg: "Installing updates"}.Encode()
	if err != nil {
		panic(err)
	}
	fmt.Printf("%q\n", seq)
	// Output: "\x1b]7501;state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\x1b\\"
}
