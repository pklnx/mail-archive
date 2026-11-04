package gobd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// indexDTD is the DTD of the description standard (GDPdU, CaseWare/IDEA)
// that index.xml names. It is not shipped: auditors' tools bring it.
const indexDTD = "gdpdu-01-09-2004.dtd"

// IndexXML is the index.xml of format v1: the three tables with their
// columns, in the structure of the description standard of the GoBD annex
// (Rz. 178): UTF-8, ";" between columns, CRLF between records, " as text
// quote, the header row skipped with Range/From 2.
func IndexXML(supplier string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE DataSet SYSTEM "` + indexDTD + `">` + "\n")
	b.WriteString("<DataSet>\n  <Version>1.0</Version>\n")
	b.WriteString("  <DataSupplier>\n")
	fmt.Fprintf(&b, "    <Name>%s</Name>\n", xmlText(supplier))
	b.WriteString("    <Location></Location>\n")
	b.WriteString("    <Comment>mail-archive GoBD export, format v1. The .eml files under mail/ are the documents; these tables are an aid.</Comment>\n")
	b.WriteString("  </DataSupplier>\n")
	b.WriteString("  <Media>\n    <Name>mail-archive export</Name>\n")
	for _, t := range tables {
		b.WriteString("    <Table>\n")
		fmt.Fprintf(&b, "      <URL>%s</URL>\n", t.file)
		fmt.Fprintf(&b, "      <Name>%s</Name>\n", t.name)
		fmt.Fprintf(&b, "      <Description>%s</Description>\n", xmlText(t.desc))
		b.WriteString("      <UTF8/>\n")
		b.WriteString("      <DecimalSymbol>,</DecimalSymbol>\n      <DigitGroupingSymbol>.</DigitGroupingSymbol>\n")
		b.WriteString("      <Range>\n        <From>2</From>\n      </Range>\n")
		b.WriteString("      <VariableLength>\n")
		b.WriteString("        <ColumnDelimiter>;</ColumnDelimiter>\n")
		b.WriteString("        <RecordDelimiter>&#13;&#10;</RecordDelimiter>\n")
		b.WriteString("        <TextEncapsulator>&quot;</TextEncapsulator>\n")
		for _, c := range t.columns {
			elem := "VariableColumn"
			if slices.Contains(t.keys, c.name) {
				elem = "VariablePrimaryKey"
			}
			fmt.Fprintf(&b, "        <%s>\n          <Name>%s</Name>\n          <Description>%s</Description>\n", elem, c.name, xmlText(c.desc))
			switch c.kind {
			case integer:
				b.WriteString("          <Numeric/>\n")
			case date:
				b.WriteString("          <Date>\n            <Format>DD.MM.YYYY</Format>\n          </Date>\n")
			default:
				b.WriteString("          <AlphaNumeric/>\n")
			}
			fmt.Fprintf(&b, "        </%s>\n", elem)
		}
		for _, col := range slices.Sorted(maps.Keys(t.refs)) {
			fmt.Fprintf(&b, "        <ForeignKey>\n          <Name>%s</Name>\n          <References>%s</References>\n        </ForeignKey>\n", col, t.refs[col])
		}
		b.WriteString("      </VariableLength>\n    </Table>\n")
	}
	b.WriteString("  </Media>\n</DataSet>\n")
	return b.Bytes()
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s)) // writing to a Builder does not fail
	return b.String()
}

// The parts of index.xml that checkIndex reads.
type xmlDataSet struct {
	Tables []xmlTable `xml:"Media>Table"`
}

type xmlTable struct {
	URL      string     `xml:"URL"`
	Name     string     `xml:"Name"`
	UTF8     *struct{}  `xml:"UTF8"`
	Variable xmlColumns `xml:"VariableLength"`
}

type xmlColumns struct {
	Delimiter   string   `xml:"ColumnDelimiter"`
	Record      string   `xml:"RecordDelimiter"`
	Encapsulate string   `xml:"TextEncapsulator"`
	Columns     []xmlCol `xml:",any"`
}

type xmlCol struct {
	XMLName xml.Name
	Name    string `xml:"Name"`
}

// checkIndex parses index.xml and checks that it declares UTF-8 and
// describes exactly the tables and columns of format v1, in order.
func checkIndex(r io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(r, maxIndexSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxIndexSize {
		return errors.New("larger than expected")
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	if !bytes.Contains(first, []byte(`encoding="UTF-8"`)) {
		return errors.New("does not declare the UTF-8 encoding")
	}
	var ds xmlDataSet
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	if err := dec.Decode(&ds); err != nil {
		return fmt.Errorf("not well-formed: %w", err)
	}
	if len(ds.Tables) != len(tables) {
		return fmt.Errorf("describes %d tables, want %d", len(ds.Tables), len(tables))
	}
	for i, t := range tables {
		x := ds.Tables[i]
		if x.URL != t.file || x.Name != t.name || x.UTF8 == nil {
			return fmt.Errorf("table %d is %q (%s), want %q (%s) in UTF-8", i+1, x.Name, x.URL, t.name, t.file)
		}
		v := x.Variable
		if v.Delimiter != ";" || v.Record != "\r\n" || v.Encapsulate != `"` {
			return fmt.Errorf("table %s: delimiters differ from the CSV files", t.name)
		}
		var names []string
		for _, c := range v.Columns {
			if c.XMLName.Local == "VariablePrimaryKey" || c.XMLName.Local == "VariableColumn" {
				names = append(names, c.Name)
			}
		}
		want := make([]string, len(t.columns))
		for j, c := range t.columns {
			want[j] = c.name
		}
		if !slices.Equal(names, want) {
			return fmt.Errorf("table %s: columns %v differ from the CSV header %v", t.name, names, want)
		}
	}
	return nil
}
