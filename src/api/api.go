package api

import (
	"bytes"
	"log"
	"net/http"
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
				URL:  v.URL,
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

const defaultIcon = "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"
