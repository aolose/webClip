package main

import "flag"
import . "webClip/api"

func main() {
	var ip = flag.String("web", "../public", "please input you web root")
	var c = flag.String("crt", "", "please input you  crt path")
	var k = flag.String("key", "", "please input you  key path")
	var u = flag.String("ca", "", "please input you  ca path")
	flag.Parse()
	Run(*ip, *c, *k, *u)
}
