package main

import (
	"flag"
	"fmt"
	"os"

	"mosquitto-plugin/internal/pluginutil"
)

var (
	salt     = flag.String("salt", "", "salt")
	password = flag.String("password", "", "password")
)

func main() {
	flag.Parse()

	if *password == "" {
		flag.Usage()
		os.Exit(2)
	}

	fmt.Println(pluginutil.SHA256PwdSalt(*password, *salt))
}
