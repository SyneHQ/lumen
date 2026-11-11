package enrich

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	geoip2 "github.com/oschwald/geoip2-golang/v2"
)

// DB-IP "IP to City Lite": free monthly MMDB, CC BY 4.0.
// Attribution required where geo data is shown: "IP Geolocation by DB-IP" (https://db-ip.com).
const (
	dbipBaseURL  = "https://download.db-ip.com/free/"
	dbipMaxBytes = 512 << 20 // cap on the decompressed file
)

var errNotFound = errors.New("not published")

type dbipUpdater struct {
	e       *Enricher
	dir     string
	baseURL string
	client  *http.Client
	now     func() time.Time
	every   time.Duration // re-check interval after a success
	backoff time.Duration // first retry delay after a failure (doubles, capped at every)
	loaded  string        // month (YYYY-MM) of the reader in use
}

// StartDBIPLite loads DB-IP City Lite in the background and keeps it current.
// It never blocks: enrichment runs without geo until a database loads. It does
// nothing (returns false) when a GEOIP_DB_PATH database is already open.
// cacheDir must be writable; it holds the decompressed file.
func (e *Enricher) StartDBIPLite(cacheDir string) bool {
	return e.startDBIP(&dbipUpdater{
		dir:     cacheDir,
		baseURL: dbipBaseURL,
		client: &http.Client{
			Timeout: 10 * time.Minute, // whole body; the file is ~100 MB gzipped
			Transport: &http.Transport{
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing non-https redirect to %s", req.URL.Host)
				}
				return nil
			},
		},
		now:     time.Now,
		every:   24 * time.Hour,
		backoff: time.Minute,
	})
}

func (e *Enricher) startDBIP(u *dbipUpdater) bool {
	if e.geoFixed {
		return false
	}
	u.e = e
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.geoMu.Lock()
	e.stopGeo = func() { cancel(); <-done }
	e.geoMu.Unlock()
	go func() {
		defer close(done)
		u.run(ctx)
	}()
	return true
}

func (u *dbipUpdater) run(ctx context.Context) {
	delay := u.backoff
	for {
		wait := u.every
		if err := u.refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("geoip: dbip-lite update failed (retry in %s): %v", delay, err)
			wait = delay
			delay = min(delay*2, u.every)
		} else {
			delay = u.backoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// refresh makes sure the current month's database (or, while that is not yet
// published, the previous month's) is loaded.
func (u *dbipUpdater) refresh(ctx context.Context) error {
	now := u.now().UTC()
	cur := now.Format("2006-01")
	prev := now.AddDate(0, 0, -now.Day()).Format("2006-01") // last day of previous month
	if u.loaded == cur {
		return nil
	}
	err := u.load(ctx, cur)
	if errors.Is(err, errNotFound) {
		if u.loaded == prev {
			return nil
		}
		err = u.load(ctx, prev)
	}
	return err
}

// load opens the cached file for month, downloading it first if needed.
func (u *dbipUpdater) load(ctx context.Context, month string) error {
	name := "dbip-city-lite-" + month + ".mmdb"
	path := filepath.Join(u.dir, name)
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return err
	}
	if err := checkPrivate(u.dir, true, os.Getuid()); err != nil {
		return fmt.Errorf("refusing GeoIP cache dir: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		if err := checkPrivate(path, false, os.Getuid()); err != nil {
			// The dir is ours, so dropping the suspect file and re-downloading is safe.
			log.Printf("geoip: discarding cached file: %v", err)
			os.Remove(path)
		} else if r, err := geoip2.Open(path); err == nil {
			u.install(r, month, path)
			return nil
		}
	}
	tmp, err := u.download(ctx, name+".gz")
	if err != nil {
		return err
	}
	r, err := geoip2.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s: invalid mmdb: %w", name, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		r.Close()
		os.Remove(tmp)
		return err
	}
	u.install(r, month, path)
	return nil
}

func (u *dbipUpdater) install(r *geoip2.Reader, month, path string) {
	u.e.swapGeo(r)
	u.loaded = month
	log.Printf("geoip: loaded DB-IP City Lite %s", month)
	// Drop other months' files; the open reader keeps its own mapping alive.
	old, _ := filepath.Glob(filepath.Join(u.dir, "dbip-city-lite-*.mmdb"))
	for _, f := range old {
		if f != path {
			os.Remove(f)
		}
	}
}

// checkPrivate rejects a path another local user could have planted or can
// rewrite: a symlink, the wrong kind, another owner, or group/world-writable.
func checkPrivate(path string, wantDir bool, uid int) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", path)
	case wantDir && !fi.IsDir():
		return fmt.Errorf("%s is not a directory", path)
	case !wantDir && !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s is group/world-writable (%v)", path, fi.Mode().Perm())
	}
	if owner, ok := fileOwner(fi); ok && owner != uid {
		return fmt.Errorf("%s is owned by uid %d, not %d", path, owner, uid)
	}
	return nil
}

// download streams url+name through gunzip into a temp file in the cache dir.
func (u *dbipUpdater) download(ctx context.Context, name string) (string, error) {
	if !strings.HasPrefix(u.baseURL, "https://") {
		return "", errors.New("dbip base URL must be https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.baseURL+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%s: %w", name, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	f, err := os.CreateTemp(u.dir, ".dbip-*.tmp")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(gz, dbipMaxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > dbipMaxBytes {
		err = fmt.Errorf("decompressed size exceeds %d bytes", dbipMaxBytes)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return f.Name(), nil
}
