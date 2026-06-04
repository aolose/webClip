package main

import (
	"flag"
	"webClip/api"
)

func main() {
	webRoot := flag.String("web", "../public", "please input your web root")
	crt := flag.String("crt", "", "please input your crt path")
	key := flag.String("key", "", "please input your key path")
	ca := flag.String("ca", "", "please input your ca path")
	flag.Parse()
	api.Run(*webRoot, *crt, *key, *ca)
}
