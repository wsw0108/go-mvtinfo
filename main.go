package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"runtime"

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
)

type layerInfo struct {
	Name  string
	Count uint64
}

type tileInfo struct {
	Size   uint64
	SizeZ  uint64
	Layers []layerInfo
}

type layerCount struct {
	layer string
	min   uint64
	max   uint64
	total uint64
	tile  int
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
		)
		layer2CountMap := make(map[string]*layerCount)
		for info := range ch {
			if info.Size < minTileSize {
				minTileSize = info.Size
			}
			if info.Size > maxTileSize {
				maxTileSize = info.Size
			}
			totalTileSize += info.Size
			for _, linfo := range info.Layers {
				if count, ok := layer2CountMap[linfo.Name]; !ok {
					layer2CountMap[linfo.Name] = &layerCount{
						min:   linfo.Count,
						max:   linfo.Count,
						total: linfo.Count,
						tile:  1,
					}
				} else {
					if linfo.Count < count.min {
						count.min = linfo.Count
					}
					if linfo.Count > count.max {
						count.max = linfo.Count
					}
					count.total += linfo.Count
					count.tile += 1
				}
			}
		}
		avgTileSize := float64(totalTileSize) / float64(tileCount)
		fmt.Printf("Zoom=%d\n", z)
		fmt.Printf("Tile(count=%d):\n", tileCount)
		fmt.Println("MinSize\tMaxSize\tAvgSize")
		fmt.Printf("%d\t%d\t%.2f\n", minTileSize, maxTileSize, avgTileSize)
		var counts layerCounts
		for layer, count := range layer2CountMap {
			counts = append(counts, layerCount{
				layer: layer,
				min:   count.min,
				max:   count.max,
				total: count.total,
				tile:  count.tile,
			})
		}
		sort.Sort(counts)
		fmt.Printf("Layers(count=%d):\n", len(counts))
		fmt.Println(" MinCount\tMaxCount\tAvgCount\tLayer")
		for _, count := range counts {
			avg := float64(count.total) / float64(count.tile)
			fmt.Printf(" %d\t\t%d\t\t%.2f\t\t%s(cross=%d)\n", count.min, count.max, avg, count.layer, count.tile)
		}
		done <- struct{}{}
	}()

	var wg sync.WaitGroup
	wg.Add(tileCount)
	for x := min.X; x <= max.X; x++ {
		for y := min.Y; y <= max.Y; y++ {
			go func(z, x, y int) {
				defer wg.Done()
				u := url
				u = strings.Replace(u, "{z}", strconv.Itoa(z), -1)
				u = strings.Replace(u, "{x}", strconv.Itoa(x), -1)
				u = strings.Replace(u, "{y}", strconv.Itoa(y), -1)
				getTileInfo(u, ch)
			}(int(z), int(x), int(y))
		}
	}
	wg.Wait()
	close(ch)

	<-done
}

func getTileInfo(u string, ch chan tileInfo) {
	resp, err := http.Get(u)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	layers, err := mvt.Unmarshal(data)
	if err != nil {
		panic(err)
	}
	var layerInfos []layerInfo
	for _, layer := range layers {
		layerInfos = append(layerInfos, layerInfo{
			Name:  layer.Name,
			Count: uint64(len(layer.Features)),
		})
	}
	info := tileInfo{
		Size:   uint64(len(data)),
		Layers: layerInfos,
	}
	ch <- info
}
