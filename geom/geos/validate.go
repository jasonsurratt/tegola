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
	"errors"
	"fmt"

	"github.com/go-spatial/tegola/geom"
)

func CropAndCleanGeometry(g geom.Geometry, extent *geom.Extent) (geo geom.Geometry, err error) {

	// We may be able to cache the geos context if it is a big bottleneck.
	geos := NewGeos()
	defer geos.Finish()

	// convert geometry to the GEOS equivalent.
	geosG, err := geos.FromGoGeom(g)
	if err != nil {
		fmt.Println("Error with FromGoGeom: ", err)
		return nil, err
	}

	cropped := geos.Intersection(geosG, geos.BoundsPolygon(Bounds{extent.MinX(), extent.MinY(), extent.MaxX(), extent.MaxY()}))
	if cropped == nil {
		return nil, errors.New("unable perform intersection")
	}

	result, err := geos.ToGoGeom(cropped)

	if err != nil {
		return nil, err
	}

	return result, nil
}