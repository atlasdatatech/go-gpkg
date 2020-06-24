package gpkg

import (
	"fmt"
	"os"
	"strings"

	"github.com/jinzhu/gorm"

	"github.com/go-spatial/geom"
	"github.com/go-spatial/geom/encoding/wkb"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	"github.com/pkg/errors"
)

type GeoPackage struct {
	Uri string
	DB  *gorm.DB
}

func New(uri string) *GeoPackage {
	return &GeoPackage{
		Uri: uri,
	}
}

func (g *GeoPackage) Exists() bool {
	if _, err := os.Stat(g.Uri); os.IsNotExist(err) {
		return false
	}
	return true
}

func (g *GeoPackage) Size() (int64, error) {
	fi, err := os.Stat(g.Uri)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (g *GeoPackage) Init() error {
	db, err := gorm.Open("sqlite3", g.Uri)
	if err != nil {
		return err
	}
	g.DB = db
	return nil
}

func (g *GeoPackage) AutoMigrate() error {
	err := g.DB.AutoMigrate(Content{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating Content")
	}
	err = g.DB.AutoMigrate(TileMatrix{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating TileMatrix")
	}
	err = g.DB.AutoMigrate(TileMatrixSet{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating TileMatrixSet")
	}
	err = g.DB.AutoMigrate(Extension{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating Extension")
	}
	// err = g.DB.AutoMigrate(MetadataReference{}).Error
	// if err != nil {
	// 	return errors.Wrap(err, "Error migrating MetadataReference")
	// }
	err = g.DB.AutoMigrate(SpatialReferenceSystem{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating SpatialReferenceSystem")
	}
	err = g.DB.AutoMigrate(GeometryColumn{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating GeometryColumn")
	}
	return nil
}

// AutoMigrateRelatedTables creates tables used by the related tables extension.
//	- http://www.geopackage.org/18-000.html
func (g *GeoPackage) AutoMigrateRelatedTables() error {

	err := g.DB.AutoMigrate(Relation{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating Relation")
	}

	err = g.DB.AutoMigrate(Extension{}).Error
	if err != nil {
		return errors.Wrap(err, "Error migrating Extension")
	}

	extension := Extension{
		Table:      Relation{}.TableName(),
		Column:     nil,
		Extension:  "related_tables",
		Definition: "TBD",
		Scope:      "read-write",
	}
	err = g.DB.Where(extension).Assign(extension).FirstOrCreate(&extension).Error
	if err != nil {
		return errors.Wrap(err, "Error creating extension "+fmt.Sprint(extension))
	}
	return nil
}

func (g *GeoPackage) GetSpatialReferenceSystem(srs_id int) (SpatialReferenceSystem, error) {
	srs := SpatialReferenceSystem{}
	err := g.DB.First(&srs, SpatialReferenceSystem{SpatialReferenceSystemId: &srs_id}).Error
	return srs, err
}

func (g *GeoPackage) GetSpatialReferenceSystemCode(srs_id int) (string, error) {
	srs, err := g.GetSpatialReferenceSystem(srs_id)
	if err != nil {
		return "", err
	}
	return srs.Code(), nil
}

func (g *GeoPackage) QueryInt(stmt string) (int, error) {
	result := 0

	rows, err := g.DB.DB().Query(stmt)
	if err != nil {
		return result, err
	}

	if rows.Next() {
		if err := rows.Scan(&result); err != nil {
			return result, err
		}
	}

	return result, nil
}

func (g *GeoPackage) GetTileWith(table string) (int, error) {
	stmt := "SELECT tile_width FROM gpkg_tile_matrix WHERE table_name = \"%s\" ORDER BY zoom_level LIMIT 1;"
	return g.QueryInt(fmt.Sprintf(stmt, table))
}

func (g *GeoPackage) GetTileHeight(table string) (int, error) {
	stmt := "SELECT tile_height FROM gpkg_tile_matrix WHERE table_name = \"%s\" ORDER BY zoom_level LIMIT 1;"
	return g.QueryInt(fmt.Sprintf(stmt, table))
}

func (g *GeoPackage) GetExtent() (*Extent, error) {
	extent := Extent{}

	rows, err := g.DB.DB().Query("SELECT min(min_x), max(max_x), min(min_y), max(max_y) FROM gpkg_contents;")
	if err != nil {
		return &extent, err
	}

	if rows.Next() {
		if err := rows.Scan(&extent.MinX, &extent.MaxX, &extent.MinY, &extent.MaxY); err != nil {
			return &extent, err
		}
	}

	return &extent, nil
}

func (g *GeoPackage) GetGeometryType(table_name string, column_name string) (string, error) {
	geometry_type := ""

	rows, err := g.DB.DB().Query("SELECT geometry_type_name FROM gpkg_geometry_columns WHERE table_name='" + table_name + "' and column_name='" + column_name + "';")
	if err != nil {
		return "", err
	}

	if rows.Next() {
		if err := rows.Scan(&geometry_type); err != nil {
			return "", err
		}
	}

	return geometry_type, nil
}

func (g *GeoPackage) GetTile(table string, z int, x int, y int) ([]byte, error) {
	b := make([]byte, 0)

	stmt := "SELECT tile_data FROM %s WHERE zoom_level = %d and tile_column = %d and tile_row = %d LIMIT 1;"
	rows, err := g.DB.DB().Query(fmt.Sprintf(stmt, table, z, x, y))
	if err != nil {
		return b, err
	}

	if rows.Next() {
		if err := rows.Scan(&b); err != nil {
			return b, err
		}
	}

	return b, nil
}

func (g *GeoPackage) GetFeatureCollection(table_name string) (*FeatureCollection, error) {

	stmt := "SELECT * FROM %s;"
	rows, err := g.DB.DB().Query(fmt.Sprintf(stmt, table_name))
	if err != nil {
		return &FeatureCollection{}, err
	}

	features := make([]Feature, 0)

	columns, _ := rows.Columns()
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for rows.Next() {
		var featureId interface{}
		featureProperties := map[string]interface{}{}
		featureGeometry := Geometry{}
		for i, _ := range columns {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			return &FeatureCollection{}, err
		}
		for i, col := range columns {
			if col == "id" || col == "fid" {
				switch values[i].(type) {
				case []byte:
					featureId = string(values[i].([]byte))
				default:
					featureId = values[i]
				}
			} else {
				switch values[i].(type) {
				case []byte:
					geometryType, err := g.GetGeometryType(table_name, col)
					if err != nil {
						return &FeatureCollection{}, err
					}
					if len(geometryType) > 0 {
						v := values[i].([]byte)
						h, err := NewBinaryHeader(v)
						if err != nil {
							return &FeatureCollection{}, err
						}
						g, err := wkb.DecodeBytes(v[h.Size():])
						if err != nil {
							return &FeatureCollection{}, err
						}
						coordinates, err := geom.GetCoordinates(g)
						if err != nil {
							return &FeatureCollection{}, err
						}
						switch geometryType {
						case "POINT":
							featureGeometry = Geometry{Type: "Point", Coordinates: coordinates[0]}
						case "MULTIPOINT":
							featureGeometry = Geometry{Type: "MultiPoint", Coordinates: coordinates}
						case "LINESTRING":
							featureGeometry = Geometry{Type: "LineString", Coordinates: coordinates}
						case "MULTILINESTRING":
							featureGeometry = Geometry{Type: "MultiLineString", Coordinates: coordinates}
						case "POLYGON":
							featureGeometry = Geometry{Type: "Polygon", Coordinates: coordinates}
						case "MULTIPOLYGON":
							featureGeometry = Geometry{Type: "MultiPolygon", Coordinates: coordinates}
						default:
							return &FeatureCollection{}, err
						}
					} else {
						featureProperties[col] = string(values[i].([]byte))
					}
				default:
					featureProperties[col] = values[i]
				}
			}
		}

		features = append(features, NewFeature(
			featureId,
			featureProperties,
			featureGeometry))
	}

	fc := NewFeatureCollection(features)
	return &fc, nil
}

func (g *GeoPackage) GetVectorLayers() ([]VectorLayer, error) {
	vectorLayers := make([]VectorLayer, 0)

	rows, err := g.DB.DB().Query("SELECT C.table_name, G.geometry_type_name FROM gpkg_contents as C LEFT JOIN gpkg_geometry_columns AS G ON C.table_name = G.table_name WHERE C.data_type = 'features' and C.table_name != 'roads_lines' and C.table_name != 'buildings_polygons';")
	if err != nil {
		return vectorLayers, err
	}

	for rows.Next() {
		layerName := ""
		layerType := ""
		if err := rows.Scan(&layerName, &layerType); err != nil {
			return vectorLayers, err
		}
		vectorLayers = append(vectorLayers, VectorLayer{
			Name: layerName,
			Type: layerType,
		})
	}

	return vectorLayers, nil
}

func (g *GeoPackage) GetVectorLayersAsList() (*VectorLayerList, error) {
	vectorLayers, err := g.GetVectorLayers()
	if err != nil {
		return &VectorLayerList{}, err
	}
	return &VectorLayerList{vectorLayers: vectorLayers}, nil
}

func (g *GeoPackage) GetTileMatrixSets() ([]TileMatrixSet, error) {
	tileMatrixSets := make([]TileMatrixSet, 0)
	err := g.DB.Find(&tileMatrixSets).Error
	return tileMatrixSets, err
}

func (g *GeoPackage) GetTileMatrixSetsAsIterator() (*TileMatrixSetIterator, error) {
	tileMatrixSets, err := g.GetTileMatrixSets()
	if err != nil {
		return &TileMatrixSetIterator{}, err
	}
	return &TileMatrixSetIterator{tileMatrixSets: tileMatrixSets, index: 0}, nil
}

func (g *GeoPackage) GetTileMatrixSetsAsList() (*TileMatrixSetList, error) {
	tileMatrixSets, err := g.GetTileMatrixSets()
	if err != nil {
		return &TileMatrixSetList{}, err
	}
	return &TileMatrixSetList{tileMatrixSets: tileMatrixSets}, nil
}

func (g *GeoPackage) GetMaxZoom(table string) (int, error) {
	stmt := "SELECT max(zoom_level) FROM gpkg_tile_matrix WHERE table_name = \"%s\";"
	return g.QueryInt(fmt.Sprintf(stmt, table))
}

func (g *GeoPackage) Close() error {
	return g.DB.Close()
}

//AddSpatialIndex ..
func (g *GeoPackage) AddSpatialIndex(tabelName, geomColumn string) error {
	//暂时不要触发器，导入完成后在创建rtreeindex
	stmts := []string{
		`CREATE TRIGGER \"rtree_%s_%s_insert\"\n"
			"AFTER INSERT ON \"%s\"\n"
			"WHEN (new.\"%s\" NOT NULL AND NOT ST_IsEmpty(NEW.\"%s\"))\n"
			"BEGIN\n"
			"INSERT OR REPLACE INTO \"rtree_%s_%s\" VALUES (NEW.ROWID, "
			"ST_MinX(NEW.\"%s\"), ST_MaxX(NEW.\"%s\"), ST_MinY(NEW.\"%s\"), "
			"ST_MaxY(NEW.\"%s\"));\nEND`,

		`CREATE TRIGGER \"rtree_%s_%s_update1\"\n"
			"AFTER UPDATE OF \"%s\" ON \"%s\"\n"
			"WHEN OLD.ROWID = NEW.ROWID AND "
			"(NEW.\"%s\" NOT NULL AND NOT ST_IsEmpty(NEW.\"%s\"))\n"
			"BEGIN\n"
			"INSERT OR REPLACE INTO \"rtree_%s_%s\" VALUES (NEW.ROWID, "
			"ST_MinX(NEW.\"%s\"), ST_MaxX(NEW.\"%s\"), ST_MinY(NEW.\"%s\"), "
			"ST_MaxY(NEW.\"%s\"));\nEND`,

		`CREATE TRIGGER \"rtree_%s_%s_update2\"\n"
			"AFTER UPDATE OF \"%s\" ON \"%s\"\n"
			"WHEN OLD.ROWID = NEW.ROWID AND "
			"(NEW.\"%s\" IS NULL OR ST_IsEmpty(NEW.\"%s\"))\n"
			"BEGIN\n" "DELETE FROM \"rtree_%s_%s\" WHERE id = OLD.ROWID;\nEND`,

		`CREATE TRIGGER \"rtree_%s_%s_update3\"\n"
			"AFTER UPDATE OF \"%s\" ON \"%s\"\n"
			"WHEN OLD.ROWID != NEW.ROWID AND "
			"(NEW.\"%s\" NOT NULL AND NOT ST_IsEmpty(NEW.\"%s\"))\n"
			"BEGIN\n"
			"DELETE FROM \"rtree_%s_%s\" WHERE id = OLD.ROWID;\n"
			"INSERT OR REPLACE INTO \"rtree_%s_%s\" VALUES (NEW.ROWID, "
			"ST_MinX(NEW.\"%s\"), ST_MaxX(NEW.\"%s\"), ST_MinY(NEW.\"%s\"), "
			"ST_MaxY(NEW.\"%s\"));\nEND`,

		`CREATE TRIGGER \"rtree_%s_%s_update4\"\n"
			"AFTER UPDATE ON \"%s\"\n"
			"WHEN OLD.ROWID != NEW.ROWID AND "
			"(NEW.\"%s\" IS NULL OR ST_IsEmpty(NEW.\"%s\"))\n"
			"BEGIN\n"
			"DELETE FROM \"rtree_%s_%s\" WHERE id IN (OLD.ROWID, NEW.ROWID);\n"
			"END`,

		`CREATE TRIGGER \"rtree_%s_%s_delete\"\n"
			"AFTER DELETE ON \"%s\""
			"WHEN old.\"%s\" NOT NULL\n"
			"BEGIN\n" "DELETE FROM \"rtree_%s_%s\" WHERE id = OLD.ROWID;\nEND`,
	}

	fmt.Println(len(stmts))
	sql := fmt.Sprintf("CREATE VIRTUAL TABLE rtree_%s_%s USING rtree(id, minx, maxx, miny, maxy)", tabelName, geomColumn)
	err := g.DB.Exec(sql).Error
	if err != nil {
		return errors.Wrap(err, "create rtree index error")
	}
	// sql_stmt = sqlite3_mprintf ("INSERT INTO gpkg_extensions "
	// "(table_name, column_name, extension_name, definition, scope) "
	// "VALUES (Lower(%Q), Lower(%Q), 'gpkg_rtree_index', "
	// "'GeoPackage 1.0 Specification Annex L', 'write-only')",
	// table, column);
	rtrExt := &Extension{
		Table:      tabelName,
		Column:     &geomColumn,
		Extension:  "gpkg_rtree_index",
		Definition: "GeoPackage 1.0 Specification Annex L",
		Scope:      "write-only",
	}
	err = g.DB.Save(rtrExt).Error
	if err != nil {
		return errors.Wrap(err, "Add gpkg_rtree_index extension error")
	}

	return nil
}

//AddGeomColumn geom type will be covert to upper case
func (g *GeoPackage) AddGeomColumn(tableName, geomColumn, geomType string) error {
	// /* Add column definition to metadata table */
	// sql_stmt = sqlite3_mprintf ("INSERT INTO gpkg_geometry_columns "
	// "(table_name, column_name, geometry_type_name, srs_id, z, m) "
	// "VALUES (%Q, %Q, %Q, %i, %i, %i)",
	// table, geometry_column_name, geometry_type_name,
	// srid, with_z, with_m);

	supportTypes := map[string]bool{
		"GEOMETRY":        true,
		"POINT":           true,
		"LINESTRING":      true,
		"POLYGON":         true,
		"MULTIPOINT":      true,
		"MULTILINESTRING": true,
		"MULTIPOLYGON":    true,
		"GEOMCOLLECTION":  true,
	}

	upperType := strings.ToUpper(geomType)
	_, ok := supportTypes[upperType]
	if !ok {
		return fmt.Errorf("unspported geom type name")
	}

	geoCol := &GeometryColumn{
		GeometryColumnTableName:  tableName,
		ColumnName:               geomColumn,
		GeometryType:             upperType,
		SpatialReferenceSystemId: 4326,
	}
	err := g.DB.Save(geoCol).Error
	if err != nil {
		return errors.Wrap(err, "Add gpkg_geometry_columns error")
	}
	return nil
}

//InitSpatialRefSys ..
func (g *GeoPackage) InitSpatialRefSys() error {
	/* GeoPackage Section 1.1.2.1.2 */
	err := g.DB.Exec("INSERT OR REPLACE INTO gpkg_spatial_ref_sys (srs_name, srs_id, organization, organization_coordsys_id, definition) VALUES ('Undefined Cartesian', -1, 'NONE', -1, 'Undefined')").Error
	if err != nil {
		return err
	}
	err = g.DB.Exec("INSERT OR REPLACE INTO gpkg_spatial_ref_sys (srs_name, srs_id, organization, organization_coordsys_id, definition) VALUES ('Undefined Geographic', 0, 'NONE', 0, 'Undefined')").Error
	if err != nil {
		return err
	}
	err = g.DB.Exec("INSERT OR REPLACE INTO gpkg_spatial_ref_sys (srs_name, srs_id, organization, organization_coordsys_id, definition) VALUES ('WGS84', 4326, 'epsg', 4326, 'GEOGCS[\"WGS 84\",DATUM[\"WGS_1984\",SPHEROID[\"WGS 84\",6378137,298.257223563,AUTHORITY[\"EPSG\",\"7030\"]],AUTHORITY[\"EPSG\",\"6326\"]],PRIMEM[\"Greenwich\",0,AUTHORITY[\"EPSG\",\"8901\"]],UNIT[\"degree\",0.0174532925199433,AUTHORITY[\"EPSG\",\"9122\"]],AUTHORITY[\"EPSG\",\"4326\"]]')").Error
	if err != nil {
		return err
	}
	return nil
}
