package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"

	"runtime"

	"compress/gzip"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/maptile"
)

var (
	url       string
	longitude float64
	latitude  float64
	zoom      int
	offset    int
	noGzip    bool
)

type layerInfo struct {
	Name  string
	Count uint64
}

type tileInfo struct {
	X        int
	Y        int
	Size     uint64
	Features uint64
	Layers   []layerInfo
}

type layerCount struct {
	layer  string
	min    uint64
	minAtX int
	minAtY int
	max    uint64
	maxAtX int
	maxAtY int
	total  uint64
	tile   int
}

type layerCounts []layerCount

func (c layerCounts) Len() int {
	return len(c)
}

func (c layerCounts) Less(i, j int) bool {
	return c[i].layer < c[j].layer
}

func (c layerCounts) Swap(i, j int) {
	c[i], c[j] = c[j], c[i]
}

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
}

func main() {
	flag.StringVar(&url, "url", "", "url template")
	flag.Float64Var(&longitude, "lon", 120.0, "longitude in degree")
	flag.Float64Var(&latitude, "lat", 31.0, "latitude in degree")
	flag.IntVar(&zoom, "zoom", 6, "basic zoom")
	flag.IntVar(&offset, "offset", 2, "zoom offset")
	flag.BoolVar(&noGzip, "no-gzip", false, "do not use 'Accept-Encoding: gzip'")
	flag.Parse()

	tile := maptile.At(orb.Point{longitude, latitude}, maptile.Zoom(zoom))
	z := maptile.Zoom(zoom + offset)
	min, max := tile.Range(z)
	tileCount := int(max.X-min.X+1) * int(max.Y-min.Y+1)

	ch := make(chan tileInfo)
	done := make(chan struct{})

	go func() {
		var (
			minTileSize   uint64 = math.MaxUint64
			maxTileSize   uint64 = 0
			totalTileSize uint64
			minFeatures   uint64 = math.MaxUint64
			maxFeatures   uint64 = 0
			totalFeatures uint64
		)
		var (
			minTileSizeAtX int
			minTileSizeAtY int
			maxTileSizeAtX int
			maxTileSizeAtY int
			minFeaturesAtX int
			minFeaturesAtY int
			maxFeaturesAtX int
			maxFeaturesAtY int
		)
		layer2CountMap := make(map[string]*layerCount)
		for info := range ch {
			if info.Size < minTileSize {
				minTileSize = info.Size
				minTileSizeAtX = info.X
				minTileSizeAtY = info.Y
			}
			if info.Size > maxTileSize {
				maxTileSize = info.Size
				maxTileSizeAtX = info.X
				maxTileSizeAtY = info.Y
			}
			totalTileSize += info.Size
			if info.Features < minFeatures {
				minFeatures = info.Features
				minFeaturesAtX = info.X
				minFeaturesAtY = info.Y
			}
			if info.Features > maxFeatures {
				maxFeatures = info.Features
				maxFeaturesAtX = info.X
				maxFeaturesAtY = info.Y
			}
			totalFeatures += info.Features
			for _, linfo := range info.Layers {
				if count, ok := layer2CountMap[linfo.Name]; !ok {
					layer2CountMap[linfo.Name] = &layerCount{
						min:    linfo.Count,
						minAtX: info.X,
						minAtY: info.Y,
						max:    linfo.Count,
						maxAtX: info.X,
						maxAtY: info.Y,
						total:  linfo.Count,
						tile:   1,
					}
				} else {
					if linfo.Count < count.min {
						count.min = linfo.Count
						count.minAtX = info.X
						count.minAtY = info.Y
					}
					if linfo.Count > count.max {
						count.max = linfo.Count
						count.maxAtX = info.X
						count.maxAtY = info.Y
					}
					count.total += linfo.Count
					count.tile += 1
				}
			}
		}
		avgTileSize := float64(totalTileSize) / float64(tileCount)
		avgFeatures := float64(totalFeatures) / float64(tileCount)
		fmt.Printf("Tile(zoom=%d, count=%d):\n", z, tileCount)
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  MinSize\tMinSizeAt\tMaxSize\tMaxSizeAt\tAvgSize")
		fmt.Fprintf(w, "  %d\t(%d,%d)\t%d\t(%d,%d)\t%.2f\n", minTileSize, minTileSizeAtX, minTileSizeAtY, maxTileSize, maxTileSizeAtX, maxTileSizeAtY, avgTileSize)
		fmt.Fprintln(w, "  MinFeatures\tMinFeaturesAt\tMaxFeatures\tMaxFeaturesAt\tAvgFeatures")
		fmt.Fprintf(w, "  %d\t(%d,%d)\t%d\t(%d,%d)\t%.2f\n", minFeatures, minFeaturesAtX, minFeaturesAtY, maxFeatures, maxFeaturesAtX, maxFeaturesAtY, avgFeatures)
		w.Flush()
		var counts layerCounts
		for layer, count := range layer2CountMap {
			c := *count
			c.layer = layer
			counts = append(counts, c)
		}
		sort.Sort(counts)
		fmt.Printf("Layers(count=%d):\n", len(counts))
		fmt.Fprintln(w, "  Layer\tCover\tMinCount\tMinCountAt\tMaxCount\tMaxCountAt\tAvgCount")
		for _, count := range counts {
			avg := float64(count.total) / float64(count.tile)
			fmt.Fprintf(w, "  %s\t%d\t%d\t(%d,%d)\t%d\t(%d,%d)\t%.2f\n", count.layer, count.tile, count.min, count.minAtX, count.minAtY, count.max, count.maxAtX, count.maxAtY, avg)
		}
		w.Flush()
		done <- struct{}{}
	}()

	var wg sync.WaitGroup
	wg.Add(tileCount)
	for x := min.X; x <= max.X; x++ {
		for y := min.Y; y <= max.Y; y++ {
			go func(z, x, y int) {
				defer wg.Done()
				getTileInfo(z, x, y, ch)
			}(int(z), int(x), int(y))
		}
	}
	wg.Wait()
	close(ch)

	<-done
}

func getTileInfo(z, x, y int, ch chan tileInfo) {
	u := url
	u = strings.Replace(u, "{z}", strconv.Itoa(z), -1)
	u = strings.Replace(u, "{x}", strconv.Itoa(x), -1)
	u = strings.Replace(u, "{y}", strconv.Itoa(y), -1)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		panic(err)
	}
	if !noGzip {
		req.Header.Add("Accept-Encoding", "gzip")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	size := len(data)
	ce := resp.Header.Get("Content-Encoding")
	if strings.Contains(ce, "gzip") && !noGzip {
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			panic(err)
		}
		data, err = ioutil.ReadAll(r)
		if err != nil {
			panic(err)
		}
	}
	layers, err := mvt.Unmarshal(data)
	if err != nil {
		panic(err)
	}
	var features uint64
	var layerInfos []layerInfo
	for _, layer := range layers {
		count := len(layer.Features)
		features += uint64(count)
		layerInfos = append(layerInfos, layerInfo{
			Name:  layer.Name,
			Count: uint64(count),
		})
	}
	info := tileInfo{
		X:        x,
		Y:        y,
		Size:     uint64(size),
		Features: features,
		Layers:   layerInfos,
	}
	ch <- info
}
