package quack

import (
	"context"
	"strconv"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"
)

// ADBC 1.1 GetSetOptions for the database, connection and statement.
// GetOption answers StatusNotFound for an unknown key and SetOption
// StatusNotImplemented, as the ADBC spec asks.

func notFound(key string) error {
	return adbc.Error{Code: adbc.StatusNotFound, Msg: "unknown option " + strconv.Quote(key)}
}

func notImplemented(key string) error {
	return adbc.Error{Code: adbc.StatusNotImplemented, Msg: "unsupported option " + strconv.Quote(key)}
}

func parseIntOption(key, v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, adbc.Error{Code: adbc.StatusInvalidState, Msg: "option " + key + " is not an integer"}
	}
	return n, nil
}

func parseDoubleOption(key, v string) (float64, error) {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, adbc.Error{Code: adbc.StatusInvalidState, Msg: "option " + key + " is not a number"}
	}
	return n, nil
}

// secretOption reports keys whose values GetOption must not hand back.
func secretOption(key string) bool {
	k := strings.ToLower(key)
	return k == OptionToken || k == "password" || k == "token" ||
		strings.HasPrefix(k, OptionHTTPHeaderPrefix)
}

// --- database ---

func (d *databaseImpl) SetOption(key, value string) error {
	return d.SetOptions(map[string]string{key: value})
}
func (d *databaseImpl) SetOptionBytes(key string, value []byte) error { return notImplemented(key) }
func (d *databaseImpl) SetOptionInt(key string, value int64) error {
	return d.SetOption(key, strconv.FormatInt(value, 10))
}
func (d *databaseImpl) SetOptionDouble(key string, value float64) error {
	return d.SetOption(key, strconv.FormatFloat(value, 'g', -1, 64))
}
func (d *databaseImpl) GetOption(key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, ok := d.opts[key]
	if !ok || secretOption(key) {
		return "", notFound(key)
	}
	return v, nil
}
func (d *databaseImpl) GetOptionBytes(key string) ([]byte, error) { return nil, notFound(key) }
func (d *databaseImpl) GetOptionInt(key string) (int64, error) {
	v, err := d.GetOption(key)
	if err != nil {
		return 0, err
	}
	return parseIntOption(key, v)
}
func (d *databaseImpl) GetOptionDouble(key string) (float64, error) {
	v, err := d.GetOption(key)
	if err != nil {
		return 0, err
	}
	return parseDoubleOption(key, v)
}

// --- connection ---

func (c *connectionImpl) GetOption(key string) (string, error) {
	switch key {
	case adbc.OptionKeyAutoCommit:
		if c.autoCommit {
			return adbc.OptionValueEnabled, nil
		}
		return adbc.OptionValueDisabled, nil
	case adbc.OptionKeyCurrentCatalog, adbc.OptionKeyCurrentDbSchema:
		fn := "current_database()"
		if key == adbc.OptionKeyCurrentDbSchema {
			fn = "current_schema()"
		}
		result, err := c.sess.drainPrepared(context.Background(), "SELECT "+fn)
		if err != nil {
			return "", fromTransportError(err)
		}
		for _, ch := range result.Chunks {
			if ch.RowCount > 0 && len(ch.Columns) == 1 {
				if s, ok := ch.Columns[0].GetObject(0).(string); ok {
					return s, nil
				}
			}
		}
		return "", adbc.Error{Code: adbc.StatusInternal, Msg: "the server returned no " + fn}
	}
	return "", notFound(key)
}
func (c *connectionImpl) GetOptionBytes(key string) ([]byte, error) { return nil, notFound(key) }
func (c *connectionImpl) GetOptionInt(key string) (int64, error) {
	v, err := c.GetOption(key)
	if err != nil {
		return 0, err
	}
	return parseIntOption(key, v)
}
func (c *connectionImpl) GetOptionDouble(key string) (float64, error) {
	v, err := c.GetOption(key)
	if err != nil {
		return 0, err
	}
	return parseDoubleOption(key, v)
}
func (c *connectionImpl) SetOptionBytes(key string, value []byte) error { return notImplemented(key) }
func (c *connectionImpl) SetOptionInt(key string, value int64) error {
	return c.SetOption(key, strconv.FormatInt(value, 10))
}
func (c *connectionImpl) SetOptionDouble(key string, value float64) error {
	return c.SetOption(key, strconv.FormatFloat(value, 'g', -1, 64))
}

// useCatalogSchema switches the session's default catalog / schema.
func (c *connectionImpl) useCatalogSchema(key, value string) error {
	target := quoteIdent(value)
	if key == adbc.OptionKeyCurrentDbSchema {
		catalog, err := c.GetOption(adbc.OptionKeyCurrentCatalog)
		if err != nil {
			return err
		}
		target = quoteIdent(catalog) + "." + quoteIdent(value)
	}
	return fromTransportError(c.execNoResult(context.Background(), "USE "+target))
}

// --- statement ---

func (s *statementImpl) GetOption(key string) (string, error) {
	switch key {
	case adbc.OptionKeyIngestTargetTable:
		return s.targetTable, nil
	case adbc.OptionValueIngestTargetDBSchema:
		return s.targetSchema, nil
	case adbc.OptionValueIngestTargetCatalog:
		return s.targetCatalog, nil
	case adbc.OptionKeyIngestMode:
		if s.ingestMode == "" {
			return adbc.OptionValueIngestModeCreate, nil
		}
		return s.ingestMode, nil
	case adbc.OptionValueIngestTemporary:
		if s.ingestTemporary {
			return adbc.OptionValueEnabled, nil
		}
		return adbc.OptionValueDisabled, nil
	}
	return "", notFound(key)
}
func (s *statementImpl) GetOptionBytes(key string) ([]byte, error) { return nil, notFound(key) }
func (s *statementImpl) GetOptionInt(key string) (int64, error) {
	v, err := s.GetOption(key)
	if err != nil {
		return 0, err
	}
	return parseIntOption(key, v)
}
func (s *statementImpl) GetOptionDouble(key string) (float64, error) {
	v, err := s.GetOption(key)
	if err != nil {
		return 0, err
	}
	return parseDoubleOption(key, v)
}
func (s *statementImpl) SetOptionBytes(key string, value []byte) error { return notImplemented(key) }
func (s *statementImpl) SetOptionInt(key string, value int64) error {
	return s.SetOption(key, strconv.FormatInt(value, 10))
}
func (s *statementImpl) SetOptionDouble(key string, value float64) error {
	return s.SetOption(key, strconv.FormatFloat(value, 'g', -1, 64))
}

var (
	_ adbc.GetSetOptions = (*databaseImpl)(nil)
	_ adbc.GetSetOptions = (*connectionImpl)(nil)
	_ adbc.GetSetOptions = (*statementImpl)(nil)
)
