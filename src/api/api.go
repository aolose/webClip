package api

import (
	"bytes"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const tmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
    <dict>
        <key>ConsentText</key>
        <dict>
            <key>default</key>
            <string>Created with webClip</string>
        </dict>
        <key>PayloadContent</key>
        <array>
            <dict>
                 <key>FullScreen</key>
                 {{if .FullScreen}}<true/>{{else}}<false/>{{end}}
                 <key>Icon</key>
                 <data>{{.Icon}}</data>
                 {{if .IgnoreManifestScope}}
                 <key>IgnoreManifestScope</key>
                 <true/>
                 {{end}}
                 <key>IsRemovable</key>
                 {{if .IsRemovable}}<true/>{{else}}<false/>{{end}}
                 <key>Label</key>
                 <string>{{.Name}}</string>
                 <key>PayloadDescription</key>
                 <string>{{.Description}}</string>
                 <key>PayloadDisplayName</key>
                 <string>{{.DisplayName}}</string>
                 <key>PayloadIdentifier</key>
                 <string>{{.UUID}}</string>
                 <key>PayloadOrganization</key>
                 <string>{{.Org}}</string>
                 <key>PayloadType</key>
                 <string>com.apple.webClip.managed</string>
                 <key>PayloadUUID</key>
                 <string>{{.UUID}}</string>
                 <key>PayloadVersion</key>
                 <integer>1</integer>
                 <key>Precomposed</key>
                 {{if .Precomposed}}<true/>{{else}}<false/>{{end}}
                 <key>URL</key>
                 <string>{{.URL}}</string>
            </dict>
        </array>
        <key>PayloadDescription</key>
        <string>{{.Description}}</string>
        <key>PayloadDisplayName</key>
        <string>{{.DisplayName}}</string>
        <key>PayloadIdentifier</key>
        <string>{{.UUID}}</string>
        <key>PayloadOrganization</key>
        <string>{{.Org}}</string>
        <key>PayloadRemovalDisallowed</key>
        <false/>
        <key>PayloadType</key>
        <string>Configuration</string>
        <key>PayloadUUID</key>
        <string>{{.UUID}}</string>
        <key>PayloadVersion</key>
        <integer>1</integer>
    </dict>
</plist>
`

type Payload struct {
	ID                   string
	Icon                 string    `json:"icon"`
	Name                 string    `json:"name"`
	Description          string    `json:"desc"`
	URL                  string    `json:"url"`
	Org                  string    `json:"org"`
	DisplayName          string    `json:"install"`
	FullScreen           bool      `json:"fs"`
	IsRemovable          bool      `json:"rm"`
	Precomposed          bool      `json:"pc"`
	IgnoreManifestScope  bool      `json:"ims"`
	Identifier           uuid.UUID `json:"-"`
	UUID                 uuid.UUID `json:"-"`
	Raw                  []byte    `json:"-"`
	Time                 time.Time `json:"-"`
}

type SimplePayload struct {
	URL  string `json:"u"`
	Time int64  `json:"t"`
}

// sensitiveParams lists query parameter names whose values should be redacted.
var sensitiveParams = map[string]bool{
	"token": true, "key": true, "secret": true, "auth": true,
	"password": true, "passwd": true, "pass": true,
	"access_token": true, "api_key": true, "apikey": true,
	"sign": true, "signature": true, "authorization": true,
	"bearer": true, "jwt": true, "credential": true,
	"private": true, "api_secret": true, "apisecret": true,
	"client_secret": true, "clientsecret": true,
	"refresh_token": true, "refreshtoken": true,
	"session": true, "sid": true,
}

// isSensitiveParam reports whether name (case-insensitive) is a sensitive parameter.
func isSensitiveParam(name string) bool {
	return sensitiveParams[strings.ToLower(name)]
}

// sanitizeURL redacts sensitive query parameter values, strips the fragment,
// and masks embedded passwords, returning a safe-for-listing URL string.
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	// Redact sensitive query parameter values.
	if u.RawQuery != "" {
		q := u.Query()
		for k, vs := range q {
			if isSensitiveParam(k) {
				for i, v := range vs {
					if len(v) > 3 {
						vs[i] = "***"
					}
				}
				q[k] = vs
			}
		}
		u.RawQuery = q.Encode()
	}

	// Strip fragment.
	u.Fragment = ""

	// Mask embedded password.
	if u.User != nil {
		if pass, hasPass := u.User.Password(); hasPass && pass != "" {
			u.User = url.UserPassword(u.User.Username(), "***")
		}
	}

	return u.String()
}

var (
	cache   = make(map[string]Payload)
	cacheMu sync.RWMutex
)

// clean periodically removes entries older than 24 hours.
func clean() {
	for range time.Tick(30 * time.Second) {
		cutoff := time.Now().Add(-24 * time.Hour)
		cacheMu.Lock()
		for k, v := range cache {
			if cutoff.After(v.Time) {
				delete(cache, k)
				log.Printf("clean: deleted %s", v.URL)
			}
		}
		cacheMu.Unlock()
	}
}

// signPayload signs the rendered plist with openssl smime.
func signPayload(p *Payload, t *template.Template, crt, key, ca string) ([]byte, error) {
	re, err := regexp.Compile(`[^0-9a-zA-Z.]`)
	if err != nil {
		return nil, err
	}

	f, err := os.CreateTemp("", re.ReplaceAllString(p.URL, ""))
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())

	if err := t.Execute(f, p); err != nil {
		return nil, err
	}
	_ = f.Close()

	out, err := exec.Command("openssl",
		"smime", "-sign",
		"-in", f.Name(),
		"-signer", crt,
		"-inkey", key,
		"-certfile", ca,
		"-outform", "der",
		"-nodetach",
	).Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func Run(webRoot string, crt string, key string, ca string) {
	go clean()

	tmp, err := template.New("cfg.xml").Parse(tmpl)
	if err != nil {
		log.Fatalf("template parse error: %v", err)
	}

	router := gin.Default()

	api := router.Group("/i")

	// Redirect /i to the jump page.
	if webRoot != "" {
		api.GET("", func(c *gin.Context) {
			c.File(webRoot + "/jump.html")
		})
	}

	// Remove cached payload(s).
	api.POST("/rm", func(c *gin.Context) {
		url := c.Request.URL.RawQuery
		cacheMu.Lock()
		if url == "" {
			for k := range cache {
				delete(cache, k)
			}
		} else {
			for k, v := range cache {
				if v.URL == url {
					delete(cache, k)
				}
			}
		}
		cacheMu.Unlock()
		c.Status(http.StatusOK)
	})

	// List cached payloads.
	api.GET("/ls", func(c *gin.Context) {
		cacheMu.RLock()
		defer cacheMu.RUnlock()
		a := make([]SimplePayload, 0, len(cache))
		for _, v := range cache {
			a = append(a, SimplePayload{
				URL:  sanitizeURL(v.URL),
				Time: v.Time.Unix(),
			})
		}
		c.JSON(http.StatusOK, a)
	})

	ext := ".mobileconfig"
	cType := "application/x-apple-aspen-config"

	// Download a generated config.
	api.GET("/cfg", func(c *gin.Context) {
		uu := c.Request.URL.RawQuery
		cacheMu.RLock()
		p, ok := cache[uu]
		cacheMu.RUnlock()
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}

		header := c.Writer.Header()
		header.Set("Content-Disposition", "attachment; filename="+
			strings.ReplaceAll(p.Name, " ", ".")+ext)

		if p.Raw != nil {
			c.Data(http.StatusOK, cType, p.Raw)
			return
		}

		if key != "" && crt != "" && ca != "" {
			signed, err := signPayload(&p, tmp, crt, key, ca)
			if err != nil {
				log.Printf("signing failed: %v", err)
			} else {
				p.Raw = signed
				cacheMu.Lock()
				cache[uu] = p
				cacheMu.Unlock()
				c.Data(http.StatusOK, cType, signed)
				return
			}
		}

		var b bytes.Buffer
		if err := tmp.Execute(&b, p); err != nil {
			log.Printf("template execute error: %v", err)
			c.Status(http.StatusInternalServerError)
			return
		}
		p.Raw = b.Bytes()
		cacheMu.Lock()
		cache[uu] = p
		cacheMu.Unlock()
		c.Data(http.StatusOK, cType, p.Raw)
	})

	// Create a new webclip config.
	api.POST("/cfg", func(c *gin.Context) {
		var d Payload
		if err := c.ShouldBindJSON(&d); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": err.Error()})
			return
		}

		d.Time = time.Now()
		d.UUID = uuid.New()
		d.ID = strconv.FormatInt(d.Time.Unix(), 16)

		if d.Icon == "" {
			d.Icon = defaultIcon
		}

		cacheMu.Lock()
		// Reuse existing UUID/ID for the same URL.
		for k, v := range cache {
			if v.URL == d.URL {
				d.UUID = v.UUID
				d.ID = v.ID
				delete(cache, k)
				break
			}
		}
		cache[d.ID] = d
		cacheMu.Unlock()

		c.JSON(http.StatusOK, gin.H{"id": d.ID})
	})

	// Serve static files via NoRoute fallback so API routes take priority.
	if webRoot != "" {
		router.NoRoute(func(c *gin.Context) {
			c.File(webRoot + c.Request.URL.Path)
		})
	}

	log.Println("webClip listening on 127.0.0.1:7001")
	if err := router.Run("127.0.0.1:7001"); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

const defaultIcon = "iVBORw0KGgoAAAANSUhEUgAAALQAAAC0CAMAAAAKE/YAAAAAGXRFWHRTb2Z0d2FyZQBBZG9iZSBJbWFnZVJlYWR5ccllPAAAAxFpVFh0WE1MOmNvbS5hZG9iZS54bXAAAAAAADw/eHBhY2tldCBiZWdpbj0i77u/IiBpZD0iVzVNME1wQ2VoaUh6cmVTek5UY3prYzlkIj8+IDx4OnhtcG1ldGEgeG1sbnM6eD0iYWRvYmU6bnM6bWV0YS8iIHg6eG1wdGs9IkFkb2JlIFhNUCBDb3JlIDYuMC1jMDA2IDc5LjE2NDc1MywgMjAyMS8wMi8xNS0xMTo1MjoxMyAgICAgICAgIj4gPHJkZjpSREYgeG1sbnM6cmRmPSJodHRwOi8vd3d3LnczLm9yZy8xOTk5LzAyLzIyLXJkZi1zeW50YXgtbnMjIj4gPHJkZjpEZXNjcmlwdGlvbiByZGY6YWJvdXQ9IiIgeG1sbnM6eG1wTU09Imh0dHA6Ly9ucy5hZG9iZS5jb20veGFwLzEuMC9tbS8iIHhtbG5zOnN0UmVmPSJodHRwOi8vbnMuYWRvYmUuY29tL3hhcC8xLjAvc1R5cGUvUmVzb3VyY2VSZWYjIiB4bWxuczp4bXA9Imh0dHA6Ly9ucy5hZG9iZS5jb20veGFwLzEuMC8iIHhtcE1NOkRvY3VtZW50SUQ9InhtcC5kaWQ6MzJGRTA2MkY2RUJBMTFGMUI0RjdDRDZCOUY1MTZEOUMiIHhtcE1NOkluc3RhbmNlSUQ9InhtcC5paWQ6MzJGRTA2MkU2RUJBMTFGMUI0RjdDRDZCOUY1MTZEOUMiIHhtcDpDcmVhdG9yVG9vbD0iQWRvYmUgUGhvdG9zaG9wIDIwMjEgV2luZG93cyI+IDx4bXBNTTpEZXJpdmVkRnJvbSBzdFJlZjppbnN0YW5jZUlEPSJCMDY5MkI5OUFBNkI1NUM1Q0RGMEMwRjY5QTRDNzVFQyIgc3RSZWY6ZG9jdW1lbnRJRD0iQjA2OTJCOTlBQTZCNTVDNUNERjBDMEY2OUE0Qzc1RUMiLz4gPC9yZGY6RGVzY3JpcHRpb24+IDwvcmRmOlJERj4gPC94OnhtcG1ldGE+IDw/eHBhY2tldCBlbmQ9InIiPz7L+9k2AAAABlBMVEUAAAD///+l2Z/dAAABwElEQVR42uzcy44DIQxE0fL///RI2U9CB/wocr1utY6iFsHGRmEYAg0aNGjQoEGDBg0aNGjQoEFfjNYrrNCSHVqyQ0t2aMkPLUP0eXM+WoZoGaJliM4xg64yp6IFugadZq5FB2gvcyU6DNFhiLbIxhPNVejwQ9sUaxLNBeiMd6eis94dhlFfgDzw++vcGqGyP3UdAr+z6J+oR0trFr2LYvSiRZ+iEC0taLQWVWgdjRq01KxWO/kLtQaYH6s1wfxUPQStTLQM0SPID9FDzCPQqev0FPMAdO7eY4y5HZ28NRXordSlF52dbgn0Xjp+NVqgi4o1oI3R6QVIgQYNGjRo0KCn7UxBszUl3fp5tEAPV4MeV+rVIHV3Ud0TnXu6BbrsxHaOegY6rwtBY9QDzsafs69HB+iNj/p+dBiuHqDr0JZ9eZ5oek3L0JatyCNKkb/RqZ6gjgJ0gK4aGYlmc95E0dXoKENHqzlnSu5ydJSio9G8Ma5qcCSXom5AR5d5bwT7s6i13+OR+puHCtGLdy0dNu9fK7BoOUg+chfCMuWMOLh1AjRo0KBBgwYNGjRo0KBBgwYNGjRo0KBBgwY9Lv4EGACS3GAjriceHwAAAABJRU5ErkJggg=="