/*
 * The source code was taken from the imposm3 project:
 * https://github.com/omniscale/imposm3
 *
 * Some modifications have been made to fit Tegola.
 *
 * This source code is provided under:
 * http://www.apache.org/licenses/LICENSE-2.0
 */
package geos

import (
	"fmt"
	"io/ioutil"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/go-spatial/tegola/geom"
	"github.com/go-spatial/tegola/geom/encoding/geojson"
	"github.com/go-spatial/tegola/geom/encoding/wkb"
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
	fname := "../../maths/validate/testdata/benchmark_input_usa.wkb";
	wkbData, err := ioutil.ReadFile(fname)
	if err != nil {
		b.Errorf("Error reading WKB file: %v", err)
	}

	g, err := wkb.DecodeBytes(wkbData)
	if err != nil {
		b.Errorf("Error decoding WKB file: %v", err)
	}

	ext := geom.NewExtent(
		[2]float64{-15000000,  3000000},
		[2]float64{-11000000,  6000000},
	);

	// Benchmark the clean & crop
	start := time.Now()
	result, err := CropAndCleanGeometry(g, ext)
	if err != nil {
		b.Errorf("Error cropping & cleaning geometry: %v", err)	
	}
	elapsed := time.Now().Sub(start)
	fmt.Println("Elapsed: ", elapsed)

	fc := geojson.FeatureCollection{
		Features: []geojson.Feature{ 
			geojson.Feature{Geometry: geojson.Geometry{g}},
			geojson.Feature{Geometry: geojson.Geometry{result}},
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
