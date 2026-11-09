package quack

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/gizmodata/adbc-driver-quack/internal/quacktype"
)

// geoArrowWKB is the GeoArrow "geoarrow.wkb" extension type: WKB bytes in
// a binary column, with the CRS in the extension metadata. DuckDB's own
// Arrow export (and so Hardcharger) returns GEOMETRY this way.
type geoArrowWKB struct {
	arrow.ExtensionBase
	metadata string
}

func newGeoArrowWKB(metadata string) *geoArrowWKB {
	return &geoArrowWKB{ExtensionBase: arrow.ExtensionBase{Storage: arrow.BinaryTypes.Binary}, metadata: metadata}
}

func (*geoArrowWKB) ExtensionName() string { return "geoarrow.wkb" }
func (g *geoArrowWKB) Serialize() string   { return g.metadata }
func (*geoArrowWKB) Deserialize(storage arrow.DataType, data string) (arrow.ExtensionType, error) {
	return &geoArrowWKB{ExtensionBase: arrow.ExtensionBase{Storage: storage}, metadata: data}, nil
}
func (g *geoArrowWKB) ExtensionEquals(other arrow.ExtensionType) bool {
	o, ok := other.(*geoArrowWKB)
	return ok && o.metadata == g.metadata && arrow.TypeEqual(o.Storage, g.Storage)
}
func (*geoArrowWKB) ArrayType() reflect.Type { return reflect.TypeOf(geoArrowWKBArray{}) }

type geoArrowWKBArray struct{ array.ExtensionArrayBase }

func init() {
	// Imported Arrow data (Python, C) then arrives as this type, so ingest
	// recognises a geoarrow.wkb column as GEOMETRY. Ignore "already
	// registered" when another package registered the name first.
	_ = arrow.RegisterExtensionType(newGeoArrowWKB("{}"))
}

var (
	authCodeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*:[A-Za-z0-9_.]+$`)
	sridRE     = regexp.MustCompile(`^[0-9]+$`)
)

// geoArrowMetadata is the GeoArrow extension metadata for a GEOMETRY type:
// "{}" without a CRS, else the CRS with its crs_type, as DuckDB writes it.
// DuckDB converts a CRS to PROJJSON when it can (that needs PROJ); the
// driver passes the definition through in the form the server stored it.
func geoArrowMetadata(t quacktype.LogicalType) string {
	info, ok := t.TypeInfo.(quacktype.GeoTypeInfo)
	crs := ""
	if ok {
		crs = strings.TrimSpace(info.CRSDefinition)
	}
	if crs == "" {
		return "{}"
	}
	meta := map[string]any{"crs": crs}
	switch {
	case strings.HasPrefix(crs, "{") && json.Valid([]byte(crs)):
		meta["crs_type"] = "projjson"
		meta["crs"] = json.RawMessage(crs)
	case sridRE.MatchString(crs):
		meta["crs_type"] = "srid"
	case authCodeRE.MatchString(crs):
		meta["crs_type"] = "authority_code"
	default:
		meta["crs_type"] = "wkt2:2019"
	}
	out, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(out)
}
