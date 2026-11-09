// Copyright (c) 2026 GizmoData LLC. MIT License (see LICENSE).
// Adapted from gizmodata/hardcharger-adbc go/pkg/hardcharger/array_export.go.

//go:build driverlib

package main

import (
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/bitutil"
	"github.com/apache/arrow-go/v18/arrow/cdata"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// exportRecordBatch exports storage arrays while GetSchema exports the original
// schema (including extension metadata). Arrow Go 18.8's C Data exporter switches
// on concrete array types without unwrapping ExtensionArray: a VARIANT's struct
// schema therefore gets an array with zero children. The same issue affects
// nested extensions and extensions with other storage layouts.
func exportRecordBatch(rb arrow.RecordBatch, out *cdata.CArrowArray) {
	children := make([]arrow.ArrayData, rb.NumCols())
	for i, col := range rb.Columns() {
		children[i] = storageData(col.Data())
		defer children[i].Release()
	}
	data := array.NewData(arrow.StructOf(rb.Schema().Fields()...), int(rb.NumRows()),
		[]*memory.Buffer{nil}, children, 0, 0)
	defer data.Release()
	arr := array.NewStructData(data)
	defer arr.Release()
	cdata.ExportArrowArray(arr, out, nil)
}

// storageData builds a zero-copy view for C export only. Container field metadata
// stays in the separately exported schema; buffers, offsets and validity are
// retained exactly. Recurse through children and dictionaries so extensions work
// at any depth, not just as top-level columns.
func storageData(data arrow.ArrayData) *array.Data {
	typ := data.DataType()
	for {
		ext, ok := typ.(arrow.ExtensionType)
		if !ok {
			break
		}
		typ = ext.StorageType()
	}
	// The exporter obtains Struct.Field(), which is already sliced. Normalize
	// struct offsets so consumers don't apply the parent's offset a second time.
	offset := data.Offset()
	buffers := data.Buffers()
	normalizeStruct := typ.ID() == arrow.STRUCT && offset != 0
	if normalizeStruct {
		buffers = []*memory.Buffer{nil}
		if data.Buffers()[0] != nil {
			bitmap := make([]byte, bitutil.BytesForBits(int64(data.Len())))
			bitutil.CopyBitmap(data.Buffers()[0].Bytes(), offset, data.Len(), bitmap, 0)
			buffers[0] = memory.NewBufferBytes(bitmap)
			defer buffers[0].Release()
		}
	}
	children := make([]arrow.ArrayData, len(data.Children()))
	for i, child := range data.Children() {
		if normalizeStruct {
			sliced := array.NewSliceData(child, int64(offset), int64(offset+data.Len()))
			children[i] = storageData(sliced)
			sliced.Release()
		} else {
			children[i] = storageData(child)
		}
		defer children[i].Release()
	}
	if normalizeStruct {
		offset = 0
	}
	var dict *array.Data
	if dt, ok := typ.(*arrow.DictionaryType); ok && data.Dictionary().(*array.Data) != nil {
		dict = storageData(data.Dictionary())
		defer dict.Release()
		typ = &arrow.DictionaryType{IndexType: dt.IndexType, ValueType: dict.DataType(), Ordered: dt.Ordered}
	}
	result := array.NewData(typ, data.Len(), buffers, children, data.NullN(), offset)
	if dict != nil {
		result.SetDictionary(dict)
	}
	return result
}
