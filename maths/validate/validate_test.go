package validate

import (
	"context"
	"fmt"
	"io/ioutil"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/go-spatial/tegola/geom"
	"github.com/go-spatial/tegola/geom/encoding/geojson"
	"github.com/go-spatial/tegola/geom/encoding/wkb"
	"github.com/go-spatial/tegola/internal/convert"
)

/**
 * Benchmark clipping and complex polygon of Canada.
 * This should result in a polygon with an approximate area of 8,061,427.695 km²
 *
 * The WKB data is taken from ne_10m_admin_0_countries layer in the natural earth data.
 * https://github.com/go-spatial/tegola-osm/blob/master/natural_earth.sh
 *
 * At this time, this benchmark does not give the expected output.
 */
func BenchmarkMakeValidCanada(b *testing.B) {
	// Load the WKB data into a geometry
	fname := "testdata/benchmark_input_canada.wkb";
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

	// Create the bounding box for intersection
	ctx := context.Background()
	ext := geom.NewExtent(
		[2]float64{-12506781, 9695172},
		[2]float64{-8665112, 13332428},
	);

	// Benchmark the clean & crop
	start := time.Now()
	result, err := CleanGeometry(ctx, tegGeom, ext)
	if err != nil {
		b.Errorf("Error cleaning geometry: %v", err)	
	}
	elapsed := time.Now().Sub(start)
	fmt.Println("Elapsed: ", elapsed)

	// Conver the resulting geometry back to geom.Geometry for export
	resultGeom, err := convert.ToGeom(result)
	if err != nil {
		b.Errorf("Error converting tegola geometry to geom geometry: %v", err)
	}

	fc := geojson.FeatureCollection{
		Features: []geojson.Feature{ 
			geojson.Feature{Geometry: geojson.Geometry{g}},
			geojson.Feature{Geometry: geojson.Geometry{resultGeom}},
		},
	}

	output, err := json.Marshal(fc)
	if err != nil {
		b.Errorf("Error converting geometry to GeoJSON: %v", err)
	}

	_ = os.Mkdir("testoutput", 0755)
	f, err := os.Create("testoutput/BenchmarkMakeValidCanada.geojson")
	_, err = f.Write(output)
	if err != nil {
		b.Errorf("Error writing results to GeoJSON: %v", err)
	}
	err = f.Close()
	if err != nil {
		b.Errorf("Error closing GeoJSON file: %v", err)
	}
}
