package postgis

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx"

	"context"

	"github.com/go-spatial/tegola"
	"github.com/go-spatial/tegola/geom"
	"github.com/go-spatial/tegola/geom/encoding/wkb"
	"github.com/go-spatial/tegola/geom/slippy"
	"github.com/go-spatial/tegola/provider"
	"github.com/go-spatial/tegola/util/dict"
)

const Name = "postgis"

// Provider provides the postgis data provider.
type Provider struct {
	config pgx.ConnPoolConfig
	pool   *pgx.ConnPool
	// map of layer name and corrosponding sql
	layers     map[string]Layer
	srid       uint64
	firstlayer string
}

const (
	// We quote the field and table names to prevent colliding with postgres keywords.
	stdSQL = `SELECT %[1]v FROM %[2]v WHERE "%[3]v" && ` + bboxToken

	// SQL to get the column names, without hitting the information_schema. Though it might be better to hit the information_schema.
	fldsSQL = `SELECT * FROM %[1]v LIMIT 0;`
)

const (
	DefaultPort    = 5432
	DefaultSRID    = tegola.WebMercator
	DefaultMaxConn = 100
)

const (
	ConfigKeyHost        = "host"
	ConfigKeyPort        = "port"
	ConfigKeyDB          = "database"
	ConfigKeyUser        = "user"
	ConfigKeyPassword    = "password"
	ConfigKeyMaxConn     = "max_connections"
	ConfigKeySRID        = "srid"
	ConfigKeyLayers      = "layers"
	ConfigKeyLayerName   = "name"
	ConfigKeyTablename   = "tablename"
	ConfigKeySQL         = "sql"
	ConfigKeyFields      = "fields"
	ConfigKeyGeomField   = "geometry_fieldname"
	ConfigKeyGeomIDField = "id_fieldname"
)

func init() {
	provider.Register(Name, NewTileProvider, nil)
}

//	NewTileProvider instantiates and returns a new postgis provider or an error.
//	The function will validate that the config object looks good before
//	trying to create a driver. This Provider supports the following fields
//	in the provided map[string]interface{} map:
//
//		host (string): [Required] postgis database host
//		port (int): [Required] postgis database port (required)
//		database (string): [Required] postgis database name
//		user (string): [Required] postgis database user
//		password (string): [Required] postgis database password
//		srid (int): [Optional] The default SRID for the provider. Defaults to WebMercator (3857) but also supports WGS84 (4326)
//		max_connections : [Optional] The max connections to maintain in the connection pool. Default is 100. 0 means no max.
//		layers (map[string]struct{})  — This is map of layers keyed by the layer name. supports the following properties
//
//			name (string): [Required] the name of the layer. This is used to reference this layer from map layers.
//			tablename (string): [*Required] the name of the database table to query against. Required if sql is not defined.
//			geometry_fieldname (string): [Optional] the name of the filed which contains the geometry for the feature. defaults to geom
//			id_fieldname (string): [Optional] the name of the feature id field. defaults to gid
//			fields ([]string): [Optional] a list of fields to include alongside the feature. Can be used if sql is not defined.
//			srid (int): [Optional] the SRID of the layer. Supports 3857 (WebMercator) or 4326 (WGS84).
//			sql (string): [*Required] custom SQL to use use. Required if tablename is not defined. Supports the following tokens:
//
//				!BBOX! - [Required] will be replaced with the bounding box of the tile before the query is sent to the database.
//				!ZOOM! - [Optional] will be replaced with the "Z" (zoom) value of the requested tile.
//
func NewTileProvider(config map[string]interface{}) (provider.Tiler, error) {
	// Validate the config to make sure it has the values I care about and the types for those values.
	c := dict.M(config)

	host, err := c.String(ConfigKeyHost, nil)
	if err != nil {
		return nil, err
	}

	db, err := c.String(ConfigKeyDB, nil)
	if err != nil {
		return nil, err
	}

	user, err := c.String(ConfigKeyUser, nil)
	if err != nil {
		return nil, err
	}

	password, err := c.String(ConfigKeyPassword, nil)
	if err != nil {
		return nil, err
	}

	port := int64(DefaultPort)
	if port, err = c.Int64(ConfigKeyPort, &port); err != nil {
		return nil, err
	}

	maxcon := int64(DefaultMaxConn)
	if maxcon, err = c.Int64(ConfigKeyMaxConn, &maxcon); err != nil {
		return nil, err
	}

	var srid = int64(DefaultSRID)
	if srid, err = c.Int64(ConfigKeySRID, &srid); err != nil {
		return nil, err
	}

	p := Provider{
		srid: uint64(srid),
		config: pgx.ConnPoolConfig{
			ConnConfig: pgx.ConnConfig{
				Host:     host,
				Port:     uint16(port),
				Database: db,
				User:     user,
				Password: password,
			},
			MaxConnections: int(maxcon),
		},
	}

	if p.pool, err = pgx.NewConnPool(p.config); err != nil {
		return nil, fmt.Errorf("Failed while creating connection pool: %v", err)
	}

	layers, ok := c[ConfigKeyLayers].([]map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Expected %v to be a []map[string]interface{}", ConfigKeyLayers)
	}

	lyrs := make(map[string]Layer)
	lyrsSeen := make(map[string]int)

	for i, v := range layers {
		vc := dict.M(v)

		lname, err := vc.String(ConfigKeyLayerName, nil)
		if err != nil {
			return nil, fmt.Errorf("For layer (%v) we got the following error trying to get the layer's name field: %v", i, err)
		}
		if j, ok := lyrsSeen[lname]; ok {
			return nil, fmt.Errorf("%v layer name is duplicated in both layer %v and layer %v", lname, i, j)
		}
		lyrsSeen[lname] = i
		if i == 0 {
			p.firstlayer = lname
		}

		fields, err := vc.StringSlice(ConfigKeyFields)
		if err != nil {
			return nil, fmt.Errorf("For layer (%v) %v %v field had the following error: %v", i, lname, ConfigKeyFields, err)
		}

		geomfld := "geom"
		geomfld, err = vc.String(ConfigKeyGeomField, &geomfld)
		if err != nil {
			return nil, fmt.Errorf("For layer (%v) %v : %v", i, lname, err)
		}

		idfld := "gid"
		idfld, err = vc.String(ConfigKeyGeomIDField, &idfld)
		if err != nil {
			return nil, fmt.Errorf("For layer (%v) %v : %v", i, lname, err)
		}
		if idfld == geomfld {
			return nil, fmt.Errorf("For layer (%v) %v: %v (%v) and %v field (%v) is the same!", i, lname, ConfigKeyGeomField, geomfld, ConfigKeyGeomIDField, idfld)
		}

		var tblName string
		tblName, err = vc.String(ConfigKeyTablename, &lname)
		if err != nil {
			return nil, fmt.Errorf("for %v layer(%v) %v has an error: %v", i, lname, ConfigKeyTablename, err)
		}

		var sql string
		sql, err = vc.String(ConfigKeySQL, &sql)
		if err != nil {
			return nil, fmt.Errorf("for %v layer(%v) %v has an error: %v", i, lname, ConfigKeySQL, err)
		}

		if tblName != lname && sql != "" {
			log.Printf("Both %v and %v field are specified for layer(%v) %v, using only %[2]v field.", ConfigKeyTablename, ConfigKeySQL, i, lname)
		}

		var lsrid = srid
		if lsrid, err = vc.Int64(ConfigKeySRID, &lsrid); err != nil {
			return nil, err
		}

		l := Layer{
			name:      lname,
			idField:   idfld,
			geomField: geomfld,
			srid:      uint64(lsrid),
		}
		if sql != "" {
			// make sure that the sql has a !BBOX! token
			if !strings.Contains(sql, bboxToken) {
				return nil, fmt.Errorf("SQL for layer (%v) %v is missing required token: %v", i, lname, bboxToken)
			}
			if !strings.Contains(sql, "*") {
				if !strings.Contains(sql, geomfld) {
					return nil, fmt.Errorf("SQL for layer (%v) %v does not contain the geometry field: %v", i, lname, geomfld)
				}
				if !strings.Contains(sql, idfld) {
					return nil, fmt.Errorf("SQL for layer (%v) %v does not contain the id field for the geometry: %v", i, lname, idfld)
				}
			}
			l.sql = sql
		} else {
			// Tablename and Fields will be used to
			// We need to do some work. We need to check to see Fields contains the geom and gid fields
			// and if not add them to the list. If Fields list is empty/nil we will use '*' for the field
			// list.
			l.sql, err = genSQL(&l, p.pool, tblName, fields)
			if err != nil {
				return nil, fmt.Errorf("Could not generate sql, for layer(%v): %v", lname, err)
			}
		}
		if strings.Contains(os.Getenv("SQL_DEBUG"), "LAYER_SQL") {
			log.Printf("SQL for Layer(%v):\n%v\n", lname, l.sql)
		}

		//	set the layer geom type
		if err = p.layerGeomType(&l); err != nil {
			return nil, fmt.Errorf("error fetching geometry type for layer (%v): %v", l.name, err)
		}

		lyrs[lname] = l
	}
	p.layers = lyrs

	return p, nil
}

//	layerGeomType sets the geomType field on the layer by running the SQL and reading the geom type in the result set
func (p Provider) layerGeomType(l *Layer) error {
	var err error

	// we want to know the geom type instead of returning the geom data so we modify the SQL
	// TODO (arolek): this strategy wont work if remove the requirement of wrapping ST_AsBinary(geom) in the SQL statements.
	//
	// https://github.com/go-spatial/tegola/issues/180
	//
	// case insensitive search
	re := regexp.MustCompile(`(?i)ST_AsBinary`)
	sql := re.ReplaceAllString(l.sql, "ST_GeometryType")

	//	we only need a single result set to sniff out the geometry type
	sql = fmt.Sprintf("%v LIMIT 1", sql)

	// if a !ZOOM! token exists, all features could be filtered out so we don't have a geometry to inspect it's type.
	// address this by replacing the !ZOOM! token with an ANY statement which includes all zooms
	sql = strings.Replace(sql, "!ZOOM!", "ANY('{0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24}')", 1)

	//	we need a tile to run our sql through the replacer
	tile := slippy.NewTile(0, 0, 0, 64, tegola.WebMercator)

	//	normal replacer
	sql, err = replaceTokens(sql, l.srid, tile)
	if err != nil {
		return err
	}

	rows, err := p.pool.Query(sql)
	if err != nil {
		return err
	}
	defer rows.Close()

	//	fetch rows FieldDescriptions. this gives us the OID for the data types returned to aid in decoding
	fdescs := rows.FieldDescriptions()
	for rows.Next() {

		vals, err := rows.Values()
		if err != nil {
			return fmt.Errorf("error running SQL: %v ; %v", sql, err)
		}

		//	iterate the values returned from our row, sniffing for the geomField or st_geometrytype field name
		for i, v := range vals {
			switch fdescs[i].Name {
			case l.geomField, "st_geometrytype":
				switch v {
				case "ST_Point":
					l.geomType = geom.Point{}
				case "ST_LineString":
					l.geomType = geom.LineString{}
				case "ST_Polygon":
					l.geomType = geom.Polygon{}
				case "ST_MultiPoint":
					l.geomType = geom.MultiPoint{}
				case "ST_MultiLineString":
					l.geomType = geom.MultiLineString{}
				case "ST_MultiPolygon":
					l.geomType = geom.MultiPolygon{}
				case "ST_GeometryCollection":
					l.geomType = geom.Collection{}
				default:
					return fmt.Errorf("layer (%v) returned unsupported geometry type (%v)", l.name, v)
				}
			}
		}
	}

	return nil
}

// Layer fetches an individual layer from the provider, if it's configured
// if no name is provider, the first layer is returned
func (p *Provider) Layer(name string) (Layer, bool) {
	if name == "" {
		return p.layers[p.firstlayer], true
	}

	layer, ok := p.layers[name]
	return layer, ok
}

// Layers returns meta data about the various layers which are configured with the provider
func (p Provider) Layers() ([]provider.LayerInfo, error) {
	var ls []provider.LayerInfo

	for i := range p.layers {
		ls = append(ls, p.layers[i])
	}

	return ls, nil
}

//	TileFeatures adheres to the provider.Tiler interface
func (p Provider) TileFeatures(ctx context.Context, layer string, tile provider.Tile, fn func(f *provider.Feature) error) error {
	//	fetch the provider layer
	plyr, ok := p.Layer(layer)
	if !ok {
		return ErrLayerNotFound{layer}
	}

	sql, err := replaceTokens(plyr.sql, plyr.srid, tile)
	if err != nil {
		return fmt.Errorf("error replacing layer tokens for layer (%v) SQL (%v): %v", layer, sql, err)
	}

	if strings.Contains(os.Getenv("SQL_DEBUG"), "EXECUTE_SQL") {
		log.Printf("SQL_DEBUG:EXECUTE_SQL for layer (%v): %v", layer, sql)
	}

	// context check
	if err := ctx.Err(); err != nil {
		return err
	}

	rows, err := p.pool.Query(sql)
	if err != nil {
		return fmt.Errorf("error running layer (%v) SQL (%v): %v", layer, sql, err)
	}
	defer rows.Close()

	//	fetch rows FieldDescriptions. this gives us the OID for the data types returned to aid in decoding
	fdescs := rows.FieldDescriptions()

	for rows.Next() {
		//	context check
		if err := ctx.Err(); err != nil {
			return err
		}

		//	fetch row values
		vals, err := rows.Values()
		if err != nil {
			return fmt.Errorf("error running layer (%v) SQL (%v): %v", layer, sql, err)
		}

		gid, geobytes, tags, err := decipherFields(ctx, plyr.GeomFieldName(), plyr.IDFieldName(), fdescs, vals)
		if err != nil {
			switch err {
			case context.Canceled:
				return err
			default:
				return fmt.Errorf("For layer (%v) %v", plyr.Name(), err)
			}
		}

		//	decode our WKB
		geom, err := wkb.DecodeBytes(geobytes)
		if err != nil {
			return fmt.Errorf("unable to decode layer (%v) geometry field (%v) into wkb where (%v = %v): %v", layer, plyr.GeomFieldName(), plyr.IDFieldName(), gid, err)
		}

		feature := provider.Feature{
			ID:       gid,
			Geometry: geom,
			SRID:     plyr.SRID(),
			Tags:     tags,
		}

		//	pass the feature to the provided callback
		if err = fn(&feature); err != nil {
			return err
		}
	}

	return nil
}
