package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/foursquare/gohfile"
)

type SettingDefs struct {
	port  int
	debug bool
	mlock bool

	configJsonUrl string

	hdfsAddress    string
	hdfsPathPrefix string
	hdfsCachePath  string
}

var Settings SettingDefs

func readSettings() []string {
	s := SettingDefs{}
	flag.IntVar(&s.port, "port", 9999, "listen port")
	flag.BoolVar(&s.debug, "debug", false, "print debug output")

	flag.BoolVar(&s.mlock, "mem", false, "mlock ALL mapped files to keep them im memory (ignores = vs @ specs).")

	flag.StringVar(&s.configJsonUrl, "config-json", "", "URL of collection configuration json")

	flag.StringVar(&s.hdfsAddress, "hdfs", "", "HDFS name node to connect to.")
	flag.StringVar(&s.hdfsPathPrefix, "hdfs-prefix", "", "path-prefix indicating a file must be fetched from hdfs")
	flag.StringVar(&s.hdfsCachePath, "hdfs-cache", "/tmp", "local path to write files fetch from hdfs (*not* cleaned up automatically)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			`
Usage: %s [options] col1=path1 col2=path2 ...

	By default, collections are mmaped and locked into memory.
	Use 'col@path' to serve directly off disk.

	You may need to set 'ulimit -Hl' and 'ulimit -Sl' to allow locking.

`, os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()
	Settings = s

	if len(flag.Args()) < 1 && Settings.configJsonUrl == "" {
		log.Println("bah!", Settings)
		flag.Usage()
		os.Exit(-1)
	}

	return flag.Args()
}

func getCollectionConfig(args []string) []hfile.CollectionConfig {
	var configs []hfile.CollectionConfig
	var err error

	if Settings.configJsonUrl != "" {
		if len(args) > 0 {
			log.Fatalln("Only one of command-line collection specs or json config may be used.")
		}
		configs, err = LoadFromUrl(Settings.configJsonUrl)
		if err != nil {
			log.Fatalln(err)
		}
	} else {
		configs := make([]hfile.CollectionConfig, len(args))
		for i, pair := range flag.Args() {
			parts := strings.SplitN(pair, "=", 2)
			mlock := true
			if len(parts) != 2 {
				mlock = false
				parts = strings.SplitN(pair, "@", 2)
			}
			if len(parts) != 2 {
				log.Fatal("collections must be specified in the form 'name=path' or 'name@path'")
			}
			configs[i] = hfile.CollectionConfig{parts[0], parts[1], mlock}
		}
	}
	if Settings.mlock {
		for i, _ := range configs {
			if Settings.debug && !configs[i].Mlock {
				log.Printf("[getCollectionConfig] Forcing %s to be in-memory", configs[i].Name)
			}
			configs[i].Mlock = true
		}
	}
	return configs
}

func main() {
	args := readSettings()

	configs := getCollectionConfig(args)

	log.Println("Loading collections...")
	cs, err := hfile.LoadCollections(configs, Settings.debug)
	if err != nil {
		log.Fatal(err)
	}

	name, err := os.Hostname()
	if err != nil {
		name = "localhost"
	}
	log.Printf("Serving on http://%s:%d/ \n", name, Settings.port)

	http.Handle("/rpc/HFileService", NewHttpRpcHandler(cs))
	http.Handle("/", &DebugHandler{cs})

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", Settings.port), nil))
}