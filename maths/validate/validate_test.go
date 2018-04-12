package validate

import (
	"context"
	"fmt"
	"io/ioutil"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/geom"
	"github.com/go-spatial/tegola/geom/geos"
	"github.com/go-spatial/tegola/geom/encoding/geojson"
	"github.com/go-spatial/tegola/geom/encoding/wkb"
	"github.com/go-spatial/tegola/internal/convert"
)

type Stats struct {
	filename string;
	tegolaElapsed float64;
	geosElapsed float64;
	extentArea float64;
	originalCoordCount int32;
	geosCoordCount int32;
	tegolaCoordCount int32;
	geosArea float64;
	tegolaArea float64;
	ggOriginal geom.Geometry;
	ggGeosResult geom.Geometry;
	ggTegolaResult geom.Geometry;
	symDiffPercent float64
}

/**
 * Dump the collected benchmarking statistics to a GeoJSON file and to stdout as a TSV.
 */
func DumpStats(b *testing.B, stats []Stats) {
	fields := []string{"filename", "tegolaElapsed", "geosElapsed", "extentArea", "originalCoordCount", "geosCoordCount", "tegolaCoordCount", "geosArea", "tegolaArea", "symDiffPercent"}

	fmt.Println(strings.Join(fields[:], "\t"))

	features := []geojson.Feature{}

	for _, s := range stats {
		f1 := geojson.Feature{
			Geometry: geojson.Geometry{s.ggGeosResult},
			Properties: map[string]interface{}{},
		}
		f2 := geojson.Feature{
			Geometry: geojson.Geometry{s.ggTegolaResult},
			Properties: map[string]interface{}{},
		}

		// write the results to the screen as a TSV
		r := reflect.ValueOf(s)
		line := ""
		for _, fn := range fields {
			v := reflect.Indirect(r).FieldByName(fn)
			line = line + fmt.Sprint(v) + "\t"

			f1.Properties[fn] = fmt.Sprint(v)
			f2.Properties[fn] = fmt.Sprint(v)
		}
		fmt.Println(line)
		f1.Properties["algo"] = "geos"
		f2.Properties["algo"] = "tegola"

		// only export the features that are not geometry collections.
		// empty geometry collections tend to make QGis think the whole file doesn't contain
		// geometries.
		if _, coll := s.ggGeosResult.(geom.Collection); !coll {
			if _, coll := s.ggTegolaResult.(geom.Collection); !coll {
				features = append(features, f1)
				features = append(features, f2)
			}
		}
	}

	fc := geojson.FeatureCollection{		
		Features: features,
	}

	WriteToGeoJson(b, fc, "testoutput/RawBenchmarkStats.geojson")
}

/**
 * Write the specified feature collection out to a file.
 *
 * It is asssumed that the destination is in the testoutput directory. The testoutput
 * directory will be created, if needed.
 *
 * @param b test context
 * @param fc Feature collection to write.
 * @param fname File name to write to.
 */
func WriteToGeoJson(b *testing.B, fc geojson.FeatureCollection, fname string) {
	output, err := json.Marshal(fc)
	if err != nil {
		b.Errorf("Error converting geometry to GeoJSON: %v", err)
	}

	_ = os.Mkdir("testoutput", 0755)
	f, err := os.Create(fname)
	_, err = f.Write(output)
	if err != nil {
		b.Errorf("Error writing results to GeoJSON: %v", err)
	}
	err = f.Close()
	if err != nil {
		b.Errorf("Error closing GeoJSON file: %v", err)
	}
}

/**
 * Benchmark clipping and complex polygon of Canada.
 * This should result in a polygon with an approximate area of 8,061,427.695 km²
 *
 * The WKB data is taken from ne_10m_admin_0_countries layer in the natural earth data.
 * https://github.com/go-spatial/tegola-osm/blob/master/natural_earth.sh
 *
 * At this time, this benchmark does not give the expected output.
 */
func BenchmarkMakeValidRandom(b *testing.B) {

	files, err := filepath.Glob("testdata/*.wkb")
    if err != nil {
        b.Errorf("Error listing files in testdata/: %v", err)
    }

    var stats []Stats

    // Give random, but consistent results
    rng := rand.New(rand.NewSource(0))

	for _, tc := range files {
		// using b.Run can cause it to run the sub-test multiple times. We only want one run.
        //b.Run(tc, func(b *testing.B) {
			// Benchmark the clean & crop
        	stats = RunBenchmarkCleanGeometry(b, rng, tc, stats)
        //})
    }

    DumpStats(b, stats)
}

/**
 * Create a random extent based our the specified geometry. The center of the
 * extent will intersect the envelope of the geometry's bounding box, but there
 * is no guarantee that the extent will intersect the geometry.
 *
 * @param rng Random number generator to use
 * @param g Derive the bounds off this geometry.
 */
func CreateRandomExtent(rng *rand.Rand, g *geos.Geom) *geom.Extent {
	bbox := g.Bounds()

	width := bbox.GetWidth() * .5 * rng.Float64()
	height := bbox.GetHeight() * .5 * rng.Float64()
	centerX := bbox.MinX + bbox.GetWidth() * rng.Float64()
	centerY := bbox.MinY + bbox.GetHeight() * rng.Float64()

	return geom.NewExtent(
		[2]float64{centerX - width / 2, centerY - height / 2},
		[2]float64{centerX + width / 2, centerY + height / 2},
	)
}

/**
 * Run a benchmark for cleaning/clipping a geometry and record the statistics.
 *
 * The geometry will be run through both the Tegola cleaning/clipping function and the GEOS 
 * cleaning/clipping function. Various statistics are recorded on the time and differences between
 * the geometries.
 *
 * @return A list of gathered statistics will be appeneded to the `stats` parameter and returned as
 * 	a result.
 */
func RunBenchmarkCleanGeometry(b *testing.B, rng *rand.Rand, fname string, stats []Stats) []Stats {
	// Load the WKB data into a geometry
	wkbData, err := ioutil.ReadFile(fname)
	if err != nil {
		b.Errorf("Error reading WKB file: %v", err)
	}

	g, err := wkb.DecodeBytes(wkbData)
	if err != nil {
		b.Errorf("Error decoding WKB file: %v", err)
	}

	// convert the geometry to the tegola.Geometry equivalent.
	tegGeom, err := convert.ToTegola(g)
	if err != nil {
		b.Errorf("Error converting geometry to tegola: %v", err)
	}

	gi := geos.NewGeos()
	defer gi.Finish()

	geosGeom, err := gi.FromGoGeom(g)
	if err != nil {
		b.Errorf("Error converting geometry to geos: %v", err)
	}

	result := stats
	for i := 0; i < 20; i++ {
		result = RunOneBenchmarkCleanGeometry(b, rng, fname, result, g, tegGeom, geosGeom, gi)
	}

	return result
}

/**
 * Run a single benchmark against the specified geometry and record the results into the stats 
 * array.
 * 
 * Here I'm working with three geometry types:
 * - tegola.geom
 * - tegola
 * - geos.Geom
 *
 * To ease the inevitable confusion, I use the following prefixes:
 * - gg - tegola.Geom Geometry
 * - tg - Tegola Geometry
 * - gs - GeoS Geometry
 */
func RunOneBenchmarkCleanGeometry(b *testing.B, rng *rand.Rand, fname string, stats []Stats, ggGeom geom.Geometry, tgGeom tegola.Geometry, gsGeom *geos.Geom, gi *geos.Geos) []Stats {
	var result Stats
	result.filename = fname
	result.ggOriginal = ggGeom

	// Create a random bounding box for intersection
	ext := CreateRandomExtent(rng, gsGeom)

	// calculate the area of the intersecting box
	result.extentArea = ext.XSpan() * ext.YSpan()

	// Benchmark the tegola clean & crop
	ctx := context.Background()
	start := time.Now()
	tgTegolaResult, err := CleanGeometry(ctx, tgGeom, ext)
	if err != nil {
		b.Errorf("Error cleaning geometry: %v", err)	
	}
	result.tegolaElapsed = float64(time.Now().Sub(start)) / 1000000000

	// Benchmark the GEOS clean & crop
	start = time.Now()
	result.ggGeosResult, err = geos.CropAndCleanGeometry(ggGeom, ext)
	if err != nil {
		b.Errorf("Error cropping & cleaning geometry: %v", err)	
	}
	result.geosElapsed = float64(time.Now().Sub(start)) / 1000000000

	// convert the GEOS intersect result back to GEOS
	gsGeosResult, err := gi.FromGoGeom(result.ggGeosResult)
	if err != nil {
		b.Errorf("Error converting from go to geos: %v", err)	
	}

	// convert the Tegola result to GEOS
	result.ggTegolaResult, err = convert.ToGeom(tgTegolaResult)
	if err != nil {
		b.Errorf("Error converting from tegola to geom geometry: %v", err)		
	}

	gsTegolaResult, err := gi.FromGoGeom(result.ggTegolaResult)
	if err != nil {
		b.Errorf("Error converting from geom to geos geometry: %v", err)	
	}

	// calculate statistics using the GEOS geometries
	result.geosArea = gsGeosResult.Area()
	result.tegolaArea = gsTegolaResult.Area()

	result.originalCoordCount = gi.NumCoordinates(gsGeom)
	result.geosCoordCount = gi.NumCoordinates(gsGeosResult)
	result.tegolaCoordCount = gi.NumCoordinates(gsTegolaResult)

	// If the tegola polygon is valid, calculate the symmetric difference.
	// I found that when tegola generates very complex invalid polygons geos takes a
	// long time to fix them, so I don't try if Tegola's output is invalid.
	if (gi.IsValid(gsTegolaResult)) {
		gsIntersection := gi.Intersection(gsGeosResult, gsTegolaResult)
		intArea := gsIntersection.Area()
		symDiffArea := (result.geosArea - intArea) + (result.tegolaArea - intArea)
		result.symDiffPercent = symDiffArea / result.extentArea
	} else {
		result.symDiffPercent = -1
	}

	return append(stats, result)
}




