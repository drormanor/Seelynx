// Command onvif-events lists a camera's ONVIF event topics and prints live events.
// It reads the camera's address and login from Seelynx's database, so no password
// is typed or shown. Development tool.
//
//	onvif-events -data data -camera 101 -seconds 30
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"seelynx/internal/onvif"
	"seelynx/internal/store"
)

func main() {
	data := flag.String("data", "data", "Seelynx data directory")
	id := flag.Int64("camera", 0, "camera ID")
	seconds := flag.Int("seconds", 30, "how long to listen")
	flag.Parse()
	if err := run(*data, *id, *seconds); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var topicTag = regexp.MustCompile(`<([A-Za-z0-9_]+:)?([A-Za-z0-9_]+)[^<>]*wstop:topic="true"`)

func run(dataDir string, id int64, seconds int) error {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(dataDir, "seelynx.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	key, err := store.LoadOrCreateKey(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}
	if _, err := st.UseKey(ctx, key); err != nil {
		return err
	}
	cam, err := st.Camera(ctx, id)
	if err != nil {
		return err
	}
	u, err := url.Parse(cam.MainURL)
	if err != nil {
		return err
	}
	c := &onvif.Client{XAddr: "http://" + u.Hostname() + "/onvif/device_service", User: cam.Username, Pass: cam.Password}
	if err := c.SyncClock(ctx); err != nil {
		fmt.Println("clock read failed:", err)
	} else {
		fmt.Printf("camera clock differs from this PC by %s\n", c.Offset.Round(time.Second))
	}
	addr, err := c.EventsAddress(ctx)
	if err != nil {
		return fmt.Errorf("event service: %w", err)
	}
	fmt.Println("event service:", addr)

	props, err := c.EventProperties(ctx, addr)
	if err != nil {
		fmt.Println("event properties failed:", err)
	} else {
		seen := map[string]bool{}
		for _, m := range topicTag.FindAllSubmatch(props, -1) {
			seen[string(m[2])] = true
		}
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Println("topics:", strings.Join(names, ", "))
		if len(names) == 0 {
			fmt.Printf("raw properties (%d bytes): %.1500s\n", len(props), props)
		}
	}

	sub, err := c.Subscribe(ctx, addr, time.Duration(seconds+30)*time.Second)
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	fmt.Println("subscribed:", sub)
	end := time.Now().Add(time.Duration(seconds) * time.Second)
	total := 0
	for time.Now().Before(end) {
		events, err := c.Pull(ctx, sub, 5*time.Second)
		if err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		for _, e := range events {
			total++
			fmt.Printf("%s  %s  [%s]  %v\n", time.Now().Format("15:04:05"), e.Topic, e.Operation, e.Items)
		}
	}
	fmt.Println("events received:", total)
	return nil
}
