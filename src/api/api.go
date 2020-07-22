package api

import (
	"bytes"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	_ "github.com/satori/go.uuid"
	uuid "github.com/satori/go.uuid"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const s = `<?xml version="1.0" encoding="UTF-8"?>
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
                <true/>
                <key>Icon</key>
                <data>{{.Icon}}</data>
                <key>IsRemovable</key>
                <true/>
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
                <true/>
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
	ID          string
	Icon        string `json:"icon"`
	Name        string `json:"name"`
	Description string `json:"desc"`
	URL         string `json:"url"`
	Org         string `json:"org"`
	DisplayName string `json:"install"`
	Identifier  uuid.UUID
	UUID        uuid.UUID
	Raw         []byte
	Time        time.Time
}

type SimplePayload struct {
	URL  string `json:"u"`
	Time int64  `json:"t"`
}

var cache map[string]Payload

func clean() {
	log.Println("clean cache...")
	for {
		time.Sleep(2 * time.Second)
		n := time.Now().Add(-time.Hour * 24)
		for k, v := range cache {
			if n.After(v.Time) {
				delete(cache, k)
				log.Println("delete : " + v.URL)
			}
		}
	}
}

func Run(path string, crt string, key string, ca string) {
	go clean()
	cache = make(map[string]Payload)
	cfg := template.New("cfg.xml")
	tmp, err := cfg.Parse(s)
	if err != nil {
		log.Fatal("template error!")
	}
	router := gin.Default()
	router.Use(
		static.Serve("/", static.LocalFile(path, false)),
	)
	api := router.Group("/i")
	if path != "" {
		api.GET("", func(c *gin.Context) {
			c.File(path + "/jump.html")
		})
	}
	api.POST("/rm", func(c *gin.Context) {
		url := c.Request.URL.RawQuery
		if url == "" {
			for k, _ := range cache {
				delete(cache, k)
			}
		} else {
			for k, v := range cache {
				if v.URL == url {
					delete(cache, k)
				}
			}
		}
		c.Status(200)
	})
	api.GET("/ls", func(c *gin.Context) {
		a := make([]SimplePayload, 0)
		for _, v := range cache {
			a = append(a, SimplePayload{
				URL:  v.URL,
				Time: v.Time.Unix(),
			})
		}
		c.JSON(200, a)
	})
	ext := ".mobileconfig"
	cType := "application/x-apple-aspen-config"
	api.GET("/cfg", func(c *gin.Context) {
		uu := c.Request.URL.RawQuery
		p, ok := cache[uu]
		if ok {
			header := c.Writer.Header()
			header["Content-Disposition"] = []string{"attachment; filename=" +
				strings.ReplaceAll(p.Name, " ", ".") + ext}
			if p.Raw != nil {
				c.Data(http.StatusOK, cType, p.Raw)
				return
			}
			var err error
			if key != "" && crt != "" && ca != "" {
				re3, _ := regexp.Compile("[^0-9a-zA-Z.]")
				f, er := ioutil.TempFile("", re3.ReplaceAllString(p.URL, ""))
				err = er
				if err == nil && f != nil {
					defer os.Remove(f.Name())
					err = tmp.Execute(f, p)
					if err == nil {
						dir := f.Name()
						d := "openssl"
						a := []string{"smime", "-sign", "-in", dir, "-signer",
							crt, "-inkey", key, "-certfile", ca, "-outform", "der", "-nodetach",
						}
						out, er := exec.Command(d, a...).Output()
						err = er
						if err == nil {
							p.Raw = out
						}
					}
				}
			}
			if err != nil {
				log.Println(err)
			} else {
				if p.Raw == nil {
					var b bytes.Buffer
					err = tmp.Execute(&b, p)
					if err == nil {
						p.Raw = b.Bytes()
					} else {
						log.Println(err)
					}
				}
				c.Data(http.StatusOK, cType, p.Raw)
				return
			}
		}
		c.Status(http.StatusNotFound)
	})
	api.POST("/cfg", func(c *gin.Context) {
		d := Payload{}
		err := c.ShouldBindJSON(&d)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})
		} else {
			d.Time = time.Now()
			d.UUID = uuid.NewV4()
			d.ID = strconv.FormatInt(d.Time.Unix(), 16)
			if d.Icon == "" {
				d.Icon = "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"
			}
			for k, v := range cache {
				if v.URL == d.URL {
					d.UUID = v.UUID
					d.ID = v.ID
					delete(cache, k)
				}
			}
			cache[d.ID] = d
			c.JSON(http.StatusOK, gin.H{
				"id": d.ID,
			})
		}
	})
	_ = router.Run("127.0.0.1:7001")
}
