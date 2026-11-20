package oracle

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// TableSnapshot captures the schema columns and rows of one SQLite table.
type TableSnapshot struct {
	Name    string
	Columns []string
	Rows    []RowSnapshot
}

// RowSnapshot captures column values and SQLite typeof() storage classes for one row.
type RowSnapshot struct {
	Values map[string]any
	Types  map[string]string
}

// DBSnapshot holds snapshots for every non-internal SQLite table in a database.
type DBSnapshot struct {
	Tables map[string]TableSnapshot
}

var knownJSONColumns = map[string]bool{
	"permissions":   true,
	"value":         true,
	"tags":          true,
	"chapters":      true,
	"audioFiles":    true,
	"mediaMetadata": true,
	"settings":      true,
	"extraData":     true,
	"metadata":      true,
	"genres":        true,
	"narrators":     true,
	"authors":       true,
	"series":        true,
	"tracks":        true,
}

// SnapshotDB opens dbPath read-only and reads every non-sqlite_* table,
// capturing both column values and SQLite typeof() storage classes.
func SnapshotDB(dbPath string) (*DBSnapshot, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("stat sqlite db %s: %w", dbPath, err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }() // read-only snapshot

	tableNames, err := listSQLiteTables(db)
	if err != nil {
		return nil, err
	}

	snap := &DBSnapshot{Tables: make(map[string]TableSnapshot, len(tableNames))}
	for _, name := range tableNames {
		tbl, err := readSQLiteTable(db, name)
		if err != nil {
			return nil, fmt.Errorf("snapshot table %s: %w", name, err)
		}
		snap.Tables[name] = tbl
	}
	return snap, nil
}

func listSQLiteTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite tables: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table names: %w", err)
	}
	return names, nil
}

func readSQLiteTable(db *sql.DB, tableName string) (TableSnapshot, error) {
	cols, err := listTableColumns(db, tableName)
	if err != nil {
		return TableSnapshot{}, err
	}
	tbl := TableSnapshot{Name: tableName, Columns: cols}
	if len(cols) == 0 {
		return tbl, nil
	}

	selectParts := make([]string, 0, len(cols)*2)
	for _, c := range cols {
		q := quoteIdent(c)
		selectParts = append(selectParts, "typeof("+q+")", q)
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectParts, ", "), quoteIdent(tableName))
	rows, err := db.Query(query)
	if err != nil {
		return TableSnapshot{}, fmt.Errorf("query rows: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		types := make([]sql.NullString, len(cols))
		vals := make([]any, len(cols))
		dest := make([]any, 0, len(cols)*2)
		for i := range cols {
			dest = append(dest, &types[i], &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return TableSnapshot{}, fmt.Errorf("scan row: %w", err)
		}
		row := RowSnapshot{
			Values: make(map[string]any, len(cols)),
			Types:  make(map[string]string, len(cols)),
		}
		for i, c := range cols {
			row.Types[c] = types[i].String
			if b, ok := vals[i].([]byte); ok {
				cp := make([]byte, len(b))
				copy(cp, b)
				row.Values[c] = cp
			} else {
				row.Values[c] = vals[i]
			}
		}
		tbl.Rows = append(tbl.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return TableSnapshot{}, fmt.Errorf("iterate rows: %w", err)
	}
	return tbl, nil
}

func listTableColumns(db *sql.DB, tableName string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", quoteIdent(tableName)))
	if err != nil {
		return nil, fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scan table_info: %w", err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table_info: %w", err)
	}
	return cols, nil
}

// DiffSQLite snapshots both SQLite databases and compares tables, columns,
// row counts, SQLite typeof() storage classes, and normalized/JSON column values.
func (d *Differ) DiffSQLite(step, tsDBPath, goDBPath string) ([]Finding, error) {
	tsSnap, err := SnapshotDB(tsDBPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot ts db: %w", err)
	}
	goSnap, err := SnapshotDB(goDBPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot go db: %w", err)
	}

	tableNames := sortedUnionTableNames(tsSnap, goSnap)
	var findings []Finding
	for _, tblName := range tableNames {
		tsTbl, okTS := tsSnap.Tables[tblName]
		goTbl, okGo := goSnap.Tables[tblName]
		if !okTS {
			findings = append(findings, Finding{Step: step, Surface: "sqlite", Path: "tables." + tblName, TSValue: "<missing>", GoValue: tblName, Detail: "extra SQLite table in Go"})
			continue
		}
		if !okGo {
			findings = append(findings, Finding{Step: step, Surface: "sqlite", Path: "tables." + tblName, TSValue: tblName, GoValue: "<missing>", Detail: "missing SQLite table in Go"})
			continue
		}

		colFindings := diffColumnSets(step, tblName, tsTbl.Columns, goTbl.Columns)
		findings = append(findings, colFindings...)
		if len(colFindings) > 0 {
			continue
		}

		if len(tsTbl.Rows) != len(goTbl.Rows) {
			findings = append(findings, Finding{
				Step:    step,
				Surface: "sqlite",
				Path:    tblName + ".row_count",
				TSValue: strconv.Itoa(len(tsTbl.Rows)),
				GoValue: strconv.Itoa(len(goTbl.Rows)),
				Detail:  "row count mismatch",
			})
		}

		pairs := d.alignTableRows(tsTbl, goTbl)
		sortedCols := append([]string(nil), tsTbl.Columns...)
		sort.Strings(sortedCols)
		for idx, pair := range pairs {
			rowPath := fmt.Sprintf("%s[%d]", tblName, idx)
			if idVal, ok := pair.tsRow.Values["id"]; ok && idVal != nil {
				rowPath = fmt.Sprintf("%s[%s]", tblName, d.Corr.NormalizeString("ts", fmt.Sprintf("%v", idVal)))
			}
			for _, col := range sortedCols {
				findings = append(findings, d.diffDBCell(step, rowPath+"."+col, col, pair.tsRow, pair.goRow)...)
			}
		}
	}
	return findings, nil
}

type alignedRowPair struct {
	tsRow RowSnapshot
	goRow RowSnapshot
}

func (d *Differ) alignTableRows(tsTbl, goTbl TableSnapshot) []alignedRowPair {
	usedTS := make([]bool, len(tsTbl.Rows))
	usedGo := make([]bool, len(goTbl.Rows))
	var pairs []alignedRowPair

	if hasColumn(tsTbl.Columns, "id") {
		goByID := make(map[string]int, len(goTbl.Rows))
		for i, r := range goTbl.Rows {
			if idStr, ok := stringID(r.Values["id"]); ok {
				goByID[idStr] = i
			}
		}
		for i, tr := range tsTbl.Rows {
			tsID, ok := stringID(tr.Values["id"])
			if !ok {
				continue
			}
			if mappedGo, mapped := d.Corr.MapForGo(tsID); mapped {
				if gi, found := goByID[mappedGo]; found && !usedGo[gi] {
					usedTS[i] = true
					usedGo[gi] = true
					pairs = append(pairs, alignedRowPair{tsRow: tr, goRow: goTbl.Rows[gi]})
					continue
				}
			}
			if !isUUID(tsID) {
				if gi, found := goByID[tsID]; found && !usedGo[gi] {
					usedTS[i] = true
					usedGo[gi] = true
					pairs = append(pairs, alignedRowPair{tsRow: tr, goRow: goTbl.Rows[gi]})
				}
			}
		}
	}

	var remTS, remGo []RowSnapshot
	for i, r := range tsTbl.Rows {
		if !usedTS[i] {
			remTS = append(remTS, r)
		}
	}
	for i, r := range goTbl.Rows {
		if !usedGo[i] {
			remGo = append(remGo, r)
		}
	}

	sortRowsCanonical(d.Corr, "ts", tsTbl.Columns, remTS)
	sortRowsCanonical(d.Corr, "go", goTbl.Columns, remGo)

	n := min(len(remTS), len(remGo))
	for i := range n {
		pairs = append(pairs, alignedRowPair{tsRow: remTS[i], goRow: remGo[i]})
	}
	return pairs
}

func sortRowsCanonical(corr *Correspondence, side string, cols []string, rows []RowSnapshot) {
	sortedCols := append([]string(nil), cols...)
	sort.Strings(sortedCols)
	keys := make(map[int]string, len(rows))
	type indexedRow struct {
		row RowSnapshot
		key string
	}
	wrapped := make([]indexedRow, len(rows))
	for i, r := range rows {
		keys[i] = canonicalRowSortKey(corr, side, sortedCols, r)
		wrapped[i] = indexedRow{row: r, key: keys[i]}
	}
	sort.SliceStable(wrapped, func(i, j int) bool {
		return wrapped[i].key < wrapped[j].key
	})
	for i := range wrapped {
		rows[i] = wrapped[i].row
	}
}

func canonicalRowSortKey(corr *Correspondence, side string, sortedCols []string, row RowSnapshot) string {
	var b strings.Builder
	for _, c := range sortedCols {
		if isTimestampKey(c) || c == "refreshToken" || c == "oldRefreshToken" || c == "token" || c == "pash" {
			continue
		}
		v := row.Values[c]
		if s, ok := v.(string); ok {
			if _, okJWT := parseJWT(s); okJWT || isBcryptHash(s) {
				continue
			}
			if isUUID(s) && corr.CanonicalID(side, s) == s {
				continue
			}
			s = corr.NormalizeString(side, s)
			if c == "enclosureURL" {
				s = stripUUIDSubstrings(s)
			}
			b.WriteString(c)
			b.WriteByte('=')
			b.WriteString(s)
			b.WriteByte(';')
			continue
		}
		b.WriteString(c)
		b.WriteByte('=')
		fmt.Fprintf(&b, "%v", v)
		b.WriteByte(';')
	}
	return b.String()
}

func stripUUIDSubstrings(s string) string {
	if len(s) < 36 {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i <= len(s)-36; {
		if (i == 0 || !isHexByte(s[i-1])) && (i+36 == len(s) || !isHexByte(s[i+36])) && isUUID(s[i:i+36]) {
			b.WriteString(s[last:i])
			b.WriteString("<uuid>")
			i += 36
			last = i
			continue
		}
		i++
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (d *Differ) diffDBCell(step, path, col string, tsRow, goRow RowSnapshot) []Finding {
	tsType := tsRow.Types[col]
	goType := goRow.Types[col]
	if tsType != goType {
		return []Finding{{
			Step:    step,
			Surface: "sqlite",
			Path:    path + ".typeof",
			TSValue: tsType,
			GoValue: goType,
			Detail:  fmt.Sprintf("SQLite storage class mismatch (%s vs %s)", tsType, goType),
		}}
	}

	tsVal := tsRow.Values[col]
	goVal := goRow.Values[col]
	if tsType == "null" {
		return nil
	}

	switch tsType {
	case "integer":
		ti, _ := tsVal.(int64)
		gi, _ := goVal.(int64)
		if col == "ino" {
			return d.diffInode(step, "sqlite", path, strconv.FormatInt(ti, 10), strconv.FormatInt(gi, 10))
		}
		if col == "port" && d.Corr.MatchStubSMTPPort(ti, gi) {
			return nil
		}
		if ti != gi {
			return []Finding{{Step: step, Surface: "sqlite", Path: path, TSValue: strconv.FormatInt(ti, 10), GoValue: strconv.FormatInt(gi, 10), Detail: "SQLite integer mismatch"}}
		}
		return nil

	case "real":
		tf, _ := tsVal.(float64)
		gf, _ := goVal.(float64)
		if isTimestampKey(col) {
			if (tf == 0) != (gf == 0) || tf < 0 || gf < 0 || !sameTimestampScale(tf, gf) {
				return []Finding{{Step: step, Surface: "sqlite", Path: path, TSValue: strconv.FormatFloat(tf, 'g', -1, 64), GoValue: strconv.FormatFloat(gf, 'g', -1, 64), Detail: "SQLite timestamp real mismatch"}}
			}
			return nil
		}
		if tf != gf {
			return []Finding{{Step: step, Surface: "sqlite", Path: path, TSValue: strconv.FormatFloat(tf, 'g', -1, 64), GoValue: strconv.FormatFloat(gf, 'g', -1, 64), Detail: "SQLite real mismatch"}}
		}
		return nil

	case "text":
		tsStr, _ := tsVal.(string)
		goStr, _ := goVal.(string)
		if shouldDiffAsJSONColumn(col, tsStr, goStr) {
			tsJSON, tsErr := decodeJSONBytes([]byte(tsStr))
			goJSON, goErr := decodeJSONBytes([]byte(goStr))
			if tsErr == nil && goErr == nil {
				return d.diffJSON(step, "sqlite", path, tsJSON, goJSON)
			}
		}
		return d.diffStringValue(step, "sqlite", path, tsStr, goStr)

	case "blob":
		tb, _ := tsVal.([]byte)
		gb, _ := goVal.([]byte)
		if !bytes.Equal(tb, gb) {
			return d.diffBinary(step, "sqlite", path, tb, gb)
		}
		return nil

	default:
		tsStr := fmt.Sprintf("%v", tsVal)
		goStr := fmt.Sprintf("%v", goVal)
		if tsStr != goStr {
			return []Finding{{Step: step, Surface: "sqlite", Path: path, TSValue: tsStr, GoValue: goStr, Detail: "SQLite value mismatch"}}
		}
		return nil
	}
}

func shouldDiffAsJSONColumn(col, tsStr, goStr string) bool {
	if knownJSONColumns[col] {
		return looksLikeJSONContainer(tsStr) && looksLikeJSONContainer(goStr)
	}
	return looksLikeJSONContainer(tsStr) && looksLikeJSONContainer(goStr)
}

func looksLikeJSONContainer(s string) bool {
	trimmed := strings.TrimSpace(s)
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// DiffDBUnchanged verifies that before and after snapshots of the same database
// are completely identical across all tables, columns, storage classes, and values.
func DiffDBUnchanged(step string, before, after *DBSnapshot) []Finding {
	if before == nil || after == nil {
		return []Finding{{
			Step:    step,
			Surface: "sqlite",
			Path:    "snapshot",
			TSValue: fmt.Sprintf("%v", before != nil),
			GoValue: fmt.Sprintf("%v", after != nil),
			Detail:  "nil database snapshot",
		}}
	}

	var findings []Finding
	for _, tblName := range sortedUnionTableNames(before, after) {
		bTbl, okB := before.Tables[tblName]
		aTbl, okA := after.Tables[tblName]
		if !okB || !okA {
			findings = append(findings, Finding{
				Step:    step,
				Surface: "sqlite",
				Path:    "tables." + tblName,
				TSValue: fmt.Sprintf("%v", okB),
				GoValue: fmt.Sprintf("%v", okA),
				Detail:  "table added or removed on rejected mutation",
			})
			continue
		}
		if colDiff := diffColumnSets(step, tblName, bTbl.Columns, aTbl.Columns); len(colDiff) > 0 {
			findings = append(findings, colDiff...)
			continue
		}
		if len(bTbl.Rows) != len(aTbl.Rows) {
			findings = append(findings, Finding{
				Step:    step,
				Surface: "sqlite",
				Path:    tblName + ".row_count",
				TSValue: strconv.Itoa(len(bTbl.Rows)),
				GoValue: strconv.Itoa(len(aTbl.Rows)),
				Detail:  "row count changed on rejected mutation",
			})
			continue
		}
		sortedCols := append([]string(nil), bTbl.Columns...)
		sort.Strings(sortedCols)
		for i := range bTbl.Rows {
			br := bTbl.Rows[i]
			ar := aTbl.Rows[i]
			for _, col := range sortedCols {
				p := fmt.Sprintf("%s[%d].%s", tblName, i, col)
				if br.Types[col] != ar.Types[col] {
					findings = append(findings, Finding{
						Step:    step,
						Surface: "sqlite",
						Path:    p + ".typeof",
						TSValue: br.Types[col],
						GoValue: ar.Types[col],
						Detail:  "storage class changed on rejected mutation",
					})
					continue
				}
				if !strictDBValuesEqual(br.Values[col], ar.Values[col]) {
					findings = append(findings, Finding{
						Step:    step,
						Surface: "sqlite",
						Path:    p,
						TSValue: fmt.Sprintf("%v", br.Values[col]),
						GoValue: fmt.Sprintf("%v", ar.Values[col]),
						Detail:  "row mutated on rejected request",
					})
				}
			}
		}
	}
	return findings
}

func strictDBValuesEqual(a, b any) bool {
	if ab, okA := a.([]byte); okA {
		bb, okB := b.([]byte)
		return okB && bytes.Equal(ab, bb)
	}
	return a == b
}

func sortedUnionTableNames(a, b *DBSnapshot) []string {
	set := make(map[string]struct{}, len(a.Tables)+len(b.Tables))
	for k := range a.Tables {
		set[k] = struct{}{}
	}
	for k := range b.Tables {
		set[k] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func diffColumnSets(step, tblName string, tsCols, goCols []string) []Finding {
	tsSet := make(map[string]struct{}, len(tsCols))
	goSet := make(map[string]struct{}, len(goCols))
	all := make(map[string]struct{}, len(tsCols)+len(goCols))
	for _, c := range tsCols {
		tsSet[c] = struct{}{}
		all[c] = struct{}{}
	}
	for _, c := range goCols {
		goSet[c] = struct{}{}
		all[c] = struct{}{}
	}
	cols := make([]string, 0, len(all))
	for c := range all {
		cols = append(cols, c)
	}
	sort.Strings(cols)

	var findings []Finding
	for _, c := range cols {
		_, inTS := tsSet[c]
		_, inGo := goSet[c]
		if !inTS {
			findings = append(findings, Finding{Step: step, Surface: "sqlite", Path: tblName + ".columns." + c, TSValue: "<missing>", GoValue: c, Detail: "extra column in Go"})
		} else if !inGo {
			findings = append(findings, Finding{Step: step, Surface: "sqlite", Path: tblName + ".columns." + c, TSValue: c, GoValue: "<missing>", Detail: "missing column in Go"})
		}
	}
	return findings
}

func hasColumn(cols []string, target string) bool {
	for _, c := range cols {
		if c == target {
			return true
		}
	}
	return false
}

func stringID(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, x != ""
	case int64:
		return strconv.FormatInt(x, 10), true
	default:
		return fmt.Sprintf("%v", x), true
	}
}

func quoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
