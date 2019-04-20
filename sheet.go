// Copyright 2016 - 2019 The excelize Authors. All rights reserved. Use of
// this source code is governed by a BSD-style license that can be found in
// the LICENSE file.
//
// Package excelize providing a set of functions that allow you to write to
// and read from XLSX files. Support reads and writes XLSX file generated by
// Microsoft Excel™ 2007 and later. Support save file without losing original
// charts of XLSX. This library needs Go version 1.8 or later.

package excelize

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mohae/deepcopy"
)

// NewSheet provides function to create a new sheet by given worksheet name.
// When creating a new XLSX file, the default sheet will be created. Returns
// the number of sheets in the workbook (file) after appending the new sheet.
func (f *File) NewSheet(name string) int {
	// Check if the worksheet already exists
	if f.GetSheetIndex(name) != 0 {
		return f.SheetCount
	}
	f.DeleteSheet(name)
	f.SheetCount++
	wb := f.workbookReader()
	sheetID := 0
	for _, v := range wb.Sheets.Sheet {
		if v.SheetID > sheetID {
			sheetID = v.SheetID
		}
	}
	sheetID++
	// Update docProps/app.xml
	f.setAppXML()
	// Update [Content_Types].xml
	f.setContentTypes(sheetID)
	// Create new sheet /xl/worksheets/sheet%d.xml
	f.setSheet(sheetID, name)
	// Update xl/_rels/workbook.xml.rels
	rID := f.addXlsxWorkbookRels(sheetID)
	// Update xl/workbook.xml
	f.setWorkbook(name, sheetID, rID)
	return sheetID
}

// contentTypesReader provides a function to get the pointer to the
// [Content_Types].xml structure after deserialization.
func (f *File) contentTypesReader() *xlsxTypes {
	if f.ContentTypes == nil {
		var content xlsxTypes
		_ = xml.Unmarshal(namespaceStrictToTransitional(f.readXML("[Content_Types].xml")), &content)
		f.ContentTypes = &content
	}
	return f.ContentTypes
}

// contentTypesWriter provides a function to save [Content_Types].xml after
// serialize structure.
func (f *File) contentTypesWriter() {
	if f.ContentTypes != nil {
		output, _ := xml.Marshal(f.ContentTypes)
		f.saveFileList("[Content_Types].xml", output)
	}
}

// workbookReader provides a function to get the pointer to the xl/workbook.xml
// structure after deserialization.
func (f *File) workbookReader() *xlsxWorkbook {
	if f.WorkBook == nil {
		var content xlsxWorkbook
		_ = xml.Unmarshal(namespaceStrictToTransitional(f.readXML("xl/workbook.xml")), &content)
		f.WorkBook = &content
	}
	return f.WorkBook
}

// workBookWriter provides a function to save xl/workbook.xml after serialize
// structure.
func (f *File) workBookWriter() {
	if f.WorkBook != nil {
		output, _ := xml.Marshal(f.WorkBook)
		f.saveFileList("xl/workbook.xml", replaceRelationshipsNameSpaceBytes(output))
	}
}

// workSheetWriter provides a function to save xl/worksheets/sheet%d.xml after
// serialize structure.
func (f *File) workSheetWriter() {
	for p, sheet := range f.Sheet {
		if sheet != nil {
			for k, v := range sheet.SheetData.Row {
				f.Sheet[p].SheetData.Row[k].C = trimCell(v.C)
			}
			output, _ := xml.Marshal(sheet)
			f.saveFileList(p, replaceWorkSheetsRelationshipsNameSpaceBytes(output))
			ok := f.checked[p]
			if ok {
				f.checked[p] = false
			}
		}
	}
}

// trimCell provides a function to trim blank cells which created by completeCol.
func trimCell(column []xlsxC) []xlsxC {
	col := make([]xlsxC, len(column))
	i := 0
	for _, c := range column {
		if c.S != 0 || c.V != "" || c.F != nil || c.T != "" {
			col[i] = c
			i++
		}
	}
	return col[0:i]
}

// setContentTypes provides a function to read and update property of contents
// type of XLSX.
func (f *File) setContentTypes(index int) {
	content := f.contentTypesReader()
	content.Overrides = append(content.Overrides, xlsxOverride{
		PartName:    "/xl/worksheets/sheet" + strconv.Itoa(index) + ".xml",
		ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml",
	})
}

// setSheet provides a function to update sheet property by given index.
func (f *File) setSheet(index int, name string) {
	var xlsx xlsxWorksheet
	xlsx.Dimension.Ref = "A1"
	xlsx.SheetViews.SheetView = append(xlsx.SheetViews.SheetView, xlsxSheetView{
		WorkbookViewID: 0,
	})
	path := "xl/worksheets/sheet" + strconv.Itoa(index) + ".xml"
	f.sheetMap[trimSheetName(name)] = path
	f.Sheet[path] = &xlsx
}

// setWorkbook update workbook property of XLSX. Maximum 31 characters are
// allowed in sheet title.
func (f *File) setWorkbook(name string, sheetID, rid int) {
	content := f.workbookReader()
	content.Sheets.Sheet = append(content.Sheets.Sheet, xlsxSheet{
		Name:    trimSheetName(name),
		SheetID: sheetID,
		ID:      "rId" + strconv.Itoa(rid),
	})
}

// workbookRelsReader provides a function to read and unmarshal workbook
// relationships of XLSX file.
func (f *File) workbookRelsReader() *xlsxWorkbookRels {
	if f.WorkBookRels == nil {
		var content xlsxWorkbookRels
		_ = xml.Unmarshal(namespaceStrictToTransitional(f.readXML("xl/_rels/workbook.xml.rels")), &content)
		f.WorkBookRels = &content
	}
	return f.WorkBookRels
}

// workBookRelsWriter provides a function to save xl/_rels/workbook.xml.rels after
// serialize structure.
func (f *File) workBookRelsWriter() {
	if f.WorkBookRels != nil {
		output, _ := xml.Marshal(f.WorkBookRels)
		f.saveFileList("xl/_rels/workbook.xml.rels", output)
	}
}

// addXlsxWorkbookRels update workbook relationships property of XLSX.
func (f *File) addXlsxWorkbookRels(sheet int) int {
	content := f.workbookRelsReader()
	rID := 0
	for _, v := range content.Relationships {
		t, _ := strconv.Atoi(strings.TrimPrefix(v.ID, "rId"))
		if t > rID {
			rID = t
		}
	}
	rID++
	ID := bytes.Buffer{}
	ID.WriteString("rId")
	ID.WriteString(strconv.Itoa(rID))
	target := bytes.Buffer{}
	target.WriteString("worksheets/sheet")
	target.WriteString(strconv.Itoa(sheet))
	target.WriteString(".xml")
	content.Relationships = append(content.Relationships, xlsxWorkbookRelation{
		ID:     ID.String(),
		Target: target.String(),
		Type:   SourceRelationshipWorkSheet,
	})
	return rID
}

// setAppXML update docProps/app.xml file of XML.
func (f *File) setAppXML() {
	f.saveFileList("docProps/app.xml", []byte(templateDocpropsApp))
}

// replaceRelationshipsNameSpaceBytes; Some tools that read XLSX files have
// very strict requirements about the structure of the input XML. In
// particular both Numbers on the Mac and SAS dislike inline XML namespace
// declarations, or namespace prefixes that don't match the ones that Excel
// itself uses. This is a problem because the Go XML library doesn't multiple
// namespace declarations in a single element of a document. This function is
// a horrible hack to fix that after the XML marshalling is completed.
func replaceRelationshipsNameSpaceBytes(workbookMarshal []byte) []byte {
	oldXmlns := []byte(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	newXmlns := []byte(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x15" xmlns:x15="http://schemas.microsoft.com/office/spreadsheetml/2010/11/main">`)
	return bytes.Replace(workbookMarshal, oldXmlns, newXmlns, -1)
}

// SetActiveSheet provides function to set default active worksheet of XLSX by
// given index. Note that active index is different from the index returned by
// function GetSheetMap(). It should be greater than 0 and less than total
// worksheet numbers.
func (f *File) SetActiveSheet(index int) {
	if index < 1 {
		index = 1
	}
	wb := f.workbookReader()
	for activeTab, sheet := range wb.Sheets.Sheet {
		if sheet.SheetID == index {
			if len(wb.BookViews.WorkBookView) > 0 {
				wb.BookViews.WorkBookView[0].ActiveTab = activeTab
			} else {
				wb.BookViews.WorkBookView = append(wb.BookViews.WorkBookView, xlsxWorkBookView{
					ActiveTab: activeTab,
				})
			}
		}
	}
	for idx, name := range f.GetSheetMap() {
		xlsx, _ := f.workSheetReader(name)
		if len(xlsx.SheetViews.SheetView) > 0 {
			xlsx.SheetViews.SheetView[0].TabSelected = false
		}
		if index == idx {
			if len(xlsx.SheetViews.SheetView) > 0 {
				xlsx.SheetViews.SheetView[0].TabSelected = true
			} else {
				xlsx.SheetViews.SheetView = append(xlsx.SheetViews.SheetView, xlsxSheetView{
					TabSelected: true,
				})
			}
		}
	}
}

// GetActiveSheetIndex provides a function to get active sheet index of the
// XLSX. If not found the active sheet will be return integer 0.
func (f *File) GetActiveSheetIndex() int {
	for idx, name := range f.GetSheetMap() {
		xlsx, _ := f.workSheetReader(name)
		for _, sheetView := range xlsx.SheetViews.SheetView {
			if sheetView.TabSelected {
				return idx
			}
		}
	}
	return 0
}

// SetSheetName provides a function to set the worksheet name be given old and
// new worksheet name. Maximum 31 characters are allowed in sheet title and
// this function only changes the name of the sheet and will not update the
// sheet name in the formula or reference associated with the cell. So there
// may be problem formula error or reference missing.
func (f *File) SetSheetName(oldName, newName string) {
	oldName = trimSheetName(oldName)
	newName = trimSheetName(newName)
	content := f.workbookReader()
	for k, v := range content.Sheets.Sheet {
		if v.Name == oldName {
			content.Sheets.Sheet[k].Name = newName
			f.sheetMap[newName] = f.sheetMap[oldName]
			delete(f.sheetMap, oldName)
		}
	}
}

// GetSheetName provides a function to get worksheet name of XLSX by given
// worksheet index. If given sheet index is invalid, will return an empty
// string.
func (f *File) GetSheetName(index int) string {
	content := f.workbookReader()
	rels := f.workbookRelsReader()
	for _, rel := range rels.Relationships {
		rID, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rel.Target, "worksheets/sheet"), ".xml"))
		if rID == index {
			for _, v := range content.Sheets.Sheet {
				if v.ID == rel.ID {
					return v.Name
				}
			}
		}
	}
	return ""
}

// GetSheetIndex provides a function to get worksheet index of XLSX by given sheet
// name. If given worksheet name is invalid, will return an integer type value
// 0.
func (f *File) GetSheetIndex(name string) int {
	content := f.workbookReader()
	rels := f.workbookRelsReader()
	for _, v := range content.Sheets.Sheet {
		if v.Name == name {
			for _, rel := range rels.Relationships {
				if v.ID == rel.ID {
					rID, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rel.Target, "worksheets/sheet"), ".xml"))
					return rID
				}
			}
		}
	}
	return 0
}

// GetSheetMap provides a function to get worksheet name and index map of XLSX.
// For example:
//
//    f, err := excelize.OpenFile("./Book1.xlsx")
//    if err != nil {
//        return
//    }
//    for index, name := range f.GetSheetMap() {
//        fmt.Println(index, name)
//    }
//
func (f *File) GetSheetMap() map[int]string {
	content := f.workbookReader()
	rels := f.workbookRelsReader()
	sheetMap := map[int]string{}
	for _, v := range content.Sheets.Sheet {
		for _, rel := range rels.Relationships {
			relStr := strings.SplitN(rel.Target, "worksheets/sheet", 2)
			if rel.ID == v.ID && len(relStr) == 2 {
				rID, _ := strconv.Atoi(strings.TrimSuffix(relStr[1], ".xml"))
				sheetMap[rID] = v.Name
			}
		}
	}
	return sheetMap
}

// getSheetMap provides a function to get worksheet name and XML file path map of
// XLSX.
func (f *File) getSheetMap() map[string]string {
	maps := make(map[string]string)
	for idx, name := range f.GetSheetMap() {
		maps[name] = "xl/worksheets/sheet" + strconv.Itoa(idx) + ".xml"
	}
	return maps
}

// SetSheetBackground provides a function to set background picture by given
// worksheet name and file path.
func (f *File) SetSheetBackground(sheet, picture string) error {
	var err error
	// Check picture exists first.
	if _, err = os.Stat(picture); os.IsNotExist(err) {
		return err
	}
	ext, ok := supportImageTypes[path.Ext(picture)]
	if !ok {
		return errors.New("unsupported image extension")
	}
	file, _ := ioutil.ReadFile(picture)
	name := f.addMedia(file, ext)
	rID := f.addSheetRelationships(sheet, SourceRelationshipImage, strings.Replace(name, "xl", "..", 1), "")
	f.addSheetPicture(sheet, rID)
	f.setContentTypePartImageExtensions()
	return err
}

// DeleteSheet provides a function to delete worksheet in a workbook by given
// worksheet name. Use this method with caution, which will affect changes in
// references such as formulas, charts, and so on. If there is any referenced
// value of the deleted worksheet, it will cause a file error when you open it.
// This function will be invalid when only the one worksheet is left.
func (f *File) DeleteSheet(name string) {
	content := f.workbookReader()
	for k, v := range content.Sheets.Sheet {
		if v.Name == trimSheetName(name) && len(content.Sheets.Sheet) > 1 {
			content.Sheets.Sheet = append(content.Sheets.Sheet[:k], content.Sheets.Sheet[k+1:]...)
			sheet := "xl/worksheets/sheet" + strconv.Itoa(v.SheetID) + ".xml"
			rels := "xl/worksheets/_rels/sheet" + strconv.Itoa(v.SheetID) + ".xml.rels"
			target := f.deleteSheetFromWorkbookRels(v.ID)
			f.deleteSheetFromContentTypes(target)
			f.deleteCalcChain(v.SheetID, "") // Delete CalcChain
			delete(f.sheetMap, name)
			delete(f.XLSX, sheet)
			delete(f.XLSX, rels)
			delete(f.Sheet, sheet)
			f.SheetCount--
		}
	}
	f.SetActiveSheet(len(f.GetSheetMap()))
}

// deleteSheetFromWorkbookRels provides a function to remove worksheet
// relationships by given relationships ID in the file
// xl/_rels/workbook.xml.rels.
func (f *File) deleteSheetFromWorkbookRels(rID string) string {
	content := f.workbookRelsReader()
	for k, v := range content.Relationships {
		if v.ID == rID {
			content.Relationships = append(content.Relationships[:k], content.Relationships[k+1:]...)
			return v.Target
		}
	}
	return ""
}

// deleteSheetFromContentTypes provides a function to remove worksheet
// relationships by given target name in the file [Content_Types].xml.
func (f *File) deleteSheetFromContentTypes(target string) {
	content := f.contentTypesReader()
	for k, v := range content.Overrides {
		if v.PartName == "/xl/"+target {
			content.Overrides = append(content.Overrides[:k], content.Overrides[k+1:]...)
		}
	}
}

// CopySheet provides a function to duplicate a worksheet by gave source and
// target worksheet index. Note that currently doesn't support duplicate
// workbooks that contain tables, charts or pictures. For Example:
//
//    // Sheet1 already exists...
//    index := f.NewSheet("Sheet2")
//    err := f.CopySheet(1, index)
//    return err
//
func (f *File) CopySheet(from, to int) error {
	if from < 1 || to < 1 || from == to || f.GetSheetName(from) == "" || f.GetSheetName(to) == "" {
		return errors.New("invalid worksheet index")
	}
	return f.copySheet(from, to)
}

// copySheet provides a function to duplicate a worksheet by gave source and
// target worksheet name.
func (f *File) copySheet(from, to int) error {
	sheet, err := f.workSheetReader("sheet" + strconv.Itoa(from))
	if err != nil {
		return err
	}
	worksheet := deepcopy.Copy(sheet).(*xlsxWorksheet)
	path := "xl/worksheets/sheet" + strconv.Itoa(to) + ".xml"
	if len(worksheet.SheetViews.SheetView) > 0 {
		worksheet.SheetViews.SheetView[0].TabSelected = false
	}
	worksheet.Drawing = nil
	worksheet.TableParts = nil
	worksheet.PageSetUp = nil
	f.Sheet[path] = worksheet
	toRels := "xl/worksheets/_rels/sheet" + strconv.Itoa(to) + ".xml.rels"
	fromRels := "xl/worksheets/_rels/sheet" + strconv.Itoa(from) + ".xml.rels"
	_, ok := f.XLSX[fromRels]
	if ok {
		f.XLSX[toRels] = f.XLSX[fromRels]
	}
	return err
}

// SetSheetVisible provides a function to set worksheet visible by given worksheet
// name. A workbook must contain at least one visible worksheet. If the given
// worksheet has been activated, this setting will be invalidated. Sheet state
// values as defined by http://msdn.microsoft.com/en-us/library/office/documentformat.openxml.spreadsheet.sheetstatevalues.aspx
//
//    visible
//    hidden
//    veryHidden
//
// For example, hide Sheet1:
//
//    err := f.SetSheetVisible("Sheet1", false)
//
func (f *File) SetSheetVisible(name string, visible bool) error {
	name = trimSheetName(name)
	content := f.workbookReader()
	if visible {
		for k, v := range content.Sheets.Sheet {
			if v.Name == name {
				content.Sheets.Sheet[k].State = ""
			}
		}
		return nil
	}
	count := 0
	for _, v := range content.Sheets.Sheet {
		if v.State != "hidden" {
			count++
		}
	}
	for k, v := range content.Sheets.Sheet {
		xlsx, err := f.workSheetReader(f.GetSheetMap()[k])
		if err != nil {
			return err
		}
		tabSelected := false
		if len(xlsx.SheetViews.SheetView) > 0 {
			tabSelected = xlsx.SheetViews.SheetView[0].TabSelected
		}
		if v.Name == name && count > 1 && !tabSelected {
			content.Sheets.Sheet[k].State = "hidden"
		}
	}
	return nil
}

// parseFormatPanesSet provides a function to parse the panes settings.
func parseFormatPanesSet(formatSet string) (*formatPanes, error) {
	format := formatPanes{}
	err := json.Unmarshal([]byte(formatSet), &format)
	return &format, err
}

// SetPanes provides a function to create and remove freeze panes and split panes
// by given worksheet name and panes format set.
//
// activePane defines the pane that is active. The possible values for this
// attribute are defined in the following table:
//
//     Enumeration Value              | Description
//    --------------------------------+-------------------------------------------------------------
//     bottomLeft (Bottom Left Pane)  | Bottom left pane, when both vertical and horizontal
//                                    | splits are applied.
//                                    |
//                                    | This value is also used when only a horizontal split has
//                                    | been applied, dividing the pane into upper and lower
//                                    | regions. In that case, this value specifies the bottom
//                                    | pane.
//                                    |
//    bottomRight (Bottom Right Pane) | Bottom right pane, when both vertical and horizontal
//                                    | splits are applied.
//                                    |
//    topLeft (Top Left Pane)         | Top left pane, when both vertical and horizontal splits
//                                    | are applied.
//                                    |
//                                    | This value is also used when only a horizontal split has
//                                    | been applied, dividing the pane into upper and lower
//                                    | regions. In that case, this value specifies the top pane.
//                                    |
//                                    | This value is also used when only a vertical split has
//                                    | been applied, dividing the pane into right and left
//                                    | regions. In that case, this value specifies the left pane
//                                    |
//    topRight (Top Right Pane)       | Top right pane, when both vertical and horizontal
//                                    | splits are applied.
//                                    |
//                                    | This value is also used when only a vertical split has
//                                    | been applied, dividing the pane into right and left
//                                    | regions. In that case, this value specifies the right
//                                    | pane.
//
// Pane state type is restricted to the values supported currently listed in the following table:
//
//     Enumeration Value              | Description
//    --------------------------------+-------------------------------------------------------------
//     frozen (Frozen)                | Panes are frozen, but were not split being frozen. In
//                                    | this state, when the panes are unfrozen again, a single
//                                    | pane results, with no split.
//                                    |
//                                    | In this state, the split bars are not adjustable.
//                                    |
//     split (Split)                  | Panes are split, but not frozen. In this state, the split
//                                    | bars are adjustable by the user.
//
// x_split (Horizontal Split Position): Horizontal position of the split, in
// 1/20th of a point; 0 (zero) if none. If the pane is frozen, this value
// indicates the number of columns visible in the top pane.
//
// y_split (Vertical Split Position): Vertical position of the split, in 1/20th
// of a point; 0 (zero) if none. If the pane is frozen, this value indicates the
// number of rows visible in the left pane. The possible values for this
// attribute are defined by the W3C XML Schema double datatype.
//
// top_left_cell: Location of the top left visible cell in the bottom right pane
// (when in Left-To-Right mode).
//
// sqref (Sequence of References): Range of the selection. Can be non-contiguous
// set of ranges.
//
// An example of how to freeze column A in the Sheet1 and set the active cell on
// Sheet1!K16:
//
//    f.SetPanes("Sheet1", `{"freeze":true,"split":false,"x_split":1,"y_split":0,"top_left_cell":"B1","active_pane":"topRight","panes":[{"sqref":"K16","active_cell":"K16","pane":"topRight"}]}`)
//
// An example of how to freeze rows 1 to 9 in the Sheet1 and set the active cell
// ranges on Sheet1!A11:XFD11:
//
//    f.SetPanes("Sheet1", `{"freeze":true,"split":false,"x_split":0,"y_split":9,"top_left_cell":"A34","active_pane":"bottomLeft","panes":[{"sqref":"A11:XFD11","active_cell":"A11","pane":"bottomLeft"}]}`)
//
// An example of how to create split panes in the Sheet1 and set the active cell
// on Sheet1!J60:
//
//    f.SetPanes("Sheet1", `{"freeze":false,"split":true,"x_split":3270,"y_split":1800,"top_left_cell":"N57","active_pane":"bottomLeft","panes":[{"sqref":"I36","active_cell":"I36"},{"sqref":"G33","active_cell":"G33","pane":"topRight"},{"sqref":"J60","active_cell":"J60","pane":"bottomLeft"},{"sqref":"O60","active_cell":"O60","pane":"bottomRight"}]}`)
//
// An example of how to unfreeze and remove all panes on Sheet1:
//
//    f.SetPanes("Sheet1", `{"freeze":false,"split":false}`)
//
func (f *File) SetPanes(sheet, panes string) error {
	fs, _ := parseFormatPanesSet(panes)
	xlsx, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	p := &xlsxPane{
		ActivePane:  fs.ActivePane,
		TopLeftCell: fs.TopLeftCell,
		XSplit:      float64(fs.XSplit),
		YSplit:      float64(fs.YSplit),
	}
	if fs.Freeze {
		p.State = "frozen"
	}
	xlsx.SheetViews.SheetView[len(xlsx.SheetViews.SheetView)-1].Pane = p
	if !(fs.Freeze) && !(fs.Split) {
		if len(xlsx.SheetViews.SheetView) > 0 {
			xlsx.SheetViews.SheetView[len(xlsx.SheetViews.SheetView)-1].Pane = nil
		}
	}
	s := []*xlsxSelection{}
	for _, p := range fs.Panes {
		s = append(s, &xlsxSelection{
			ActiveCell: p.ActiveCell,
			Pane:       p.Pane,
			SQRef:      p.SQRef,
		})
	}
	xlsx.SheetViews.SheetView[len(xlsx.SheetViews.SheetView)-1].Selection = s
	return err
}

// GetSheetVisible provides a function to get worksheet visible by given worksheet
// name. For example, get visible state of Sheet1:
//
//    f.GetSheetVisible("Sheet1")
//
func (f *File) GetSheetVisible(name string) bool {
	content := f.workbookReader()
	visible := false
	for k, v := range content.Sheets.Sheet {
		if v.Name == trimSheetName(name) {
			if content.Sheets.Sheet[k].State == "" || content.Sheets.Sheet[k].State == "visible" {
				visible = true
			}
		}
	}
	return visible
}

// SearchSheet provides a function to get coordinates by given worksheet name,
// cell value, and regular expression. The function doesn't support searching
// on the calculated result, formatted numbers and conditional lookup
// currently. If it is a merged cell, it will return the coordinates of the
// upper left corner of the merged area.
//
// An example of search the coordinates of the value of "100" on Sheet1:
//
//    result, err := f.SearchSheet("Sheet1", "100")
//
// An example of search the coordinates where the numerical value in the range
// of "0-9" of Sheet1 is described:
//
//    result, err := f.SearchSheet("Sheet1", "[0-9]", true)
//
func (f *File) SearchSheet(sheet, value string, reg ...bool) ([]string, error) {
	var (
		regSearch bool
		result    []string
		inElement string
		r         xlsxRow
	)
	for _, r := range reg {
		regSearch = r
	}

	xlsx, err := f.workSheetReader(sheet)
	if err != nil {
		return result, err
	}

	name, ok := f.sheetMap[trimSheetName(sheet)]
	if !ok {
		return result, nil
	}
	if xlsx != nil {
		output, _ := xml.Marshal(f.Sheet[name])
		f.saveFileList(name, replaceWorkSheetsRelationshipsNameSpaceBytes(output))
	}
	xml.NewDecoder(bytes.NewReader(f.readXML(name)))
	d := f.sharedStringsReader()

	decoder := xml.NewDecoder(bytes.NewReader(f.readXML(name)))
	for {
		token, _ := decoder.Token()
		if token == nil {
			break
		}
		switch startElement := token.(type) {
		case xml.StartElement:
			inElement = startElement.Name.Local
			if inElement == "row" {
				r = xlsxRow{}
				_ = decoder.DecodeElement(&r, &startElement)
				for _, colCell := range r.C {
					val, _ := colCell.getValueFrom(f, d)
					if regSearch {
						regex := regexp.MustCompile(value)
						if !regex.MatchString(val) {
							continue
						}
					} else {
						if val != value {
							continue
						}
					}

					cellCol, _, err := CellNameToCoordinates(colCell.R)
					if err != nil {
						return result, err
					}
					cellName, err := CoordinatesToCellName(cellCol, r.R)
					if err != nil {
						return result, err
					}
					result = append(result, cellName)
				}
			}
		default:
		}
	}
	return result, nil
}

// ProtectSheet provides a function to prevent other users from accidentally
// or deliberately changing, moving, or deleting data in a worksheet. For
// example, protect Sheet1 with protection settings:
//
//    err := f.ProtectSheet("Sheet1", &excelize.FormatSheetProtection{
//        Password:      "password",
//        EditScenarios: false,
//    })
//
func (f *File) ProtectSheet(sheet string, settings *FormatSheetProtection) error {
	xlsx, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	if settings == nil {
		settings = &FormatSheetProtection{
			EditObjects:       true,
			EditScenarios:     true,
			SelectLockedCells: true,
		}
	}
	xlsx.SheetProtection = &xlsxSheetProtection{
		AutoFilter:          settings.AutoFilter,
		DeleteColumns:       settings.DeleteColumns,
		DeleteRows:          settings.DeleteRows,
		FormatCells:         settings.FormatCells,
		FormatColumns:       settings.FormatColumns,
		FormatRows:          settings.FormatRows,
		InsertColumns:       settings.InsertColumns,
		InsertHyperlinks:    settings.InsertHyperlinks,
		InsertRows:          settings.InsertRows,
		Objects:             settings.EditObjects,
		PivotTables:         settings.PivotTables,
		Scenarios:           settings.EditScenarios,
		SelectLockedCells:   settings.SelectLockedCells,
		SelectUnlockedCells: settings.SelectUnlockedCells,
		Sheet:               true,
		Sort:                settings.Sort,
	}
	if settings.Password != "" {
		xlsx.SheetProtection.Password = genSheetPasswd(settings.Password)
	}
	return err
}

// UnprotectSheet provides a function to unprotect an Excel worksheet.
func (f *File) UnprotectSheet(sheet string) error {
	xlsx, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	xlsx.SheetProtection = nil
	return err
}

// trimSheetName provides a function to trim invaild characters by given worksheet
// name.
func trimSheetName(name string) string {
	if strings.ContainsAny(name, ":\\/?*[]") || utf8.RuneCountInString(name) > 31 {
		r := make([]rune, 0, 31)
		for _, v := range name {
			switch v {
			case 58, 92, 47, 63, 42, 91, 93: // replace :\/?*[]
				continue
			default:
				r = append(r, v)
			}
			if len(r) == 31 {
				break
			}
		}
		name = string(r)
	}
	return name
}

// PageLayoutOption is an option of a page layout of a worksheet. See
// SetPageLayout().
type PageLayoutOption interface {
	setPageLayout(layout *xlsxPageSetUp)
}

// PageLayoutOptionPtr is a writable PageLayoutOption. See GetPageLayout().
type PageLayoutOptionPtr interface {
	PageLayoutOption
	getPageLayout(layout *xlsxPageSetUp)
}

type (
	// PageLayoutOrientation defines the orientation of page layout for a
	// worksheet.
	PageLayoutOrientation string
	// PageLayoutPaperSize defines the paper size of the worksheet
	PageLayoutPaperSize int
)

const (
	// OrientationPortrait indicates page layout orientation id portrait.
	OrientationPortrait = "portrait"
	// OrientationLandscape indicates page layout orientation id landscape.
	OrientationLandscape = "landscape"
)

// setPageLayout provides a method to set the orientation for the worksheet.
func (o PageLayoutOrientation) setPageLayout(ps *xlsxPageSetUp) {
	ps.Orientation = string(o)
}

// getPageLayout provides a method to get the orientation for the worksheet.
func (o *PageLayoutOrientation) getPageLayout(ps *xlsxPageSetUp) {
	// Excel default: portrait
	if ps == nil || ps.Orientation == "" {
		*o = OrientationPortrait
		return
	}
	*o = PageLayoutOrientation(ps.Orientation)
}

// setPageLayout provides a method to set the paper size for the worksheet.
func (p PageLayoutPaperSize) setPageLayout(ps *xlsxPageSetUp) {
	ps.PaperSize = int(p)
}

// getPageLayout provides a method to get the paper size for the worksheet.
func (p *PageLayoutPaperSize) getPageLayout(ps *xlsxPageSetUp) {
	// Excel default: 1
	if ps == nil || ps.PaperSize == 0 {
		*p = 1
		return
	}
	*p = PageLayoutPaperSize(ps.PaperSize)
}

// SetPageLayout provides a function to sets worksheet page layout.
//
// Available options:
//   PageLayoutOrientation(string)
// 	 PageLayoutPaperSize(int)
//
// The following shows the paper size sorted by excelize index number:
//
//     Index | Paper Size
//    -------+-----------------------------------------------
//       1   | Letter paper (8.5 in. by 11 in.)
//       2   | Letter small paper (8.5 in. by 11 in.)
//       3   | Tabloid paper (11 in. by 17 in.)
//       4   | Ledger paper (17 in. by 11 in.)
//       5   | Legal paper (8.5 in. by 14 in.)
//       6   | Statement paper (5.5 in. by 8.5 in.)
//       7   | Executive paper (7.25 in. by 10.5 in.)
//       8   | A3 paper (297 mm by 420 mm)
//       9   | A4 paper (210 mm by 297 mm)
//       10  | A4 small paper (210 mm by 297 mm)
//       11  | A5 paper (148 mm by 210 mm)
//       12  | B4 paper (250 mm by 353 mm)
//       13  | B5 paper (176 mm by 250 mm)
//       14  | Folio paper (8.5 in. by 13 in.)
//       15  | Quarto paper (215 mm by 275 mm)
//       16  | Standard paper (10 in. by 14 in.)
//       17  | Standard paper (11 in. by 17 in.)
//       18  | Note paper (8.5 in. by 11 in.)
//       19  | #9 envelope (3.875 in. by 8.875 in.)
//       20  | #10 envelope (4.125 in. by 9.5 in.)
//       21  | #11 envelope (4.5 in. by 10.375 in.)
//       22  | #12 envelope (4.75 in. by 11 in.)
//       23  | #14 envelope (5 in. by 11.5 in.)
//       24  | C paper (17 in. by 22 in.)
//       25  | D paper (22 in. by 34 in.)
//       26  | E paper (34 in. by 44 in.)
//       27  | DL envelope (110 mm by 220 mm)
//       28  | C5 envelope (162 mm by 229 mm)
//       29  | C3 envelope (324 mm by 458 mm)
//       30  | C4 envelope (229 mm by 324 mm)
//       31  | C6 envelope (114 mm by 162 mm)
//       32  | C65 envelope (114 mm by 229 mm)
//       33  | B4 envelope (250 mm by 353 mm)
//       34  | B5 envelope (176 mm by 250 mm)
//       35  | B6 envelope (176 mm by 125 mm)
//       36  | Italy envelope (110 mm by 230 mm)
//       37  | Monarch envelope (3.875 in. by 7.5 in.).
//       38  | 6 3/4 envelope (3.625 in. by 6.5 in.)
//       39  | US standard fanfold (14.875 in. by 11 in.)
//       40  | German standard fanfold (8.5 in. by 12 in.)
//       41  | German legal fanfold (8.5 in. by 13 in.)
//       42  | ISO B4 (250 mm by 353 mm)
//       43  | Japanese postcard (100 mm by 148 mm)
//       44  | Standard paper (9 in. by 11 in.)
//       45  | Standard paper (10 in. by 11 in.)
//       46  | Standard paper (15 in. by 11 in.)
//       47  | Invite envelope (220 mm by 220 mm)
//       50  | Letter extra paper (9.275 in. by 12 in.)
//       51  | Legal extra paper (9.275 in. by 15 in.)
//       52  | Tabloid extra paper (11.69 in. by 18 in.)
//       53  | A4 extra paper (236 mm by 322 mm)
//       54  | Letter transverse paper (8.275 in. by 11 in.)
//       55  | A4 transverse paper (210 mm by 297 mm)
//       56  | Letter extra transverse paper (9.275 in. by 12 in.)
//       57  | SuperA/SuperA/A4 paper (227 mm by 356 mm)
//       58  | SuperB/SuperB/A3 paper (305 mm by 487 mm)
//       59  | Letter plus paper (8.5 in. by 12.69 in.)
//       60  | A4 plus paper (210 mm by 330 mm)
//       61  | A5 transverse paper (148 mm by 210 mm)
//       62  | JIS B5 transverse paper (182 mm by 257 mm)
//       63  | A3 extra paper (322 mm by 445 mm)
//       64  | A5 extra paper (174 mm by 235 mm)
//       65  | ISO B5 extra paper (201 mm by 276 mm)
//       66  | A2 paper (420 mm by 594 mm)
//       67  | A3 transverse paper (297 mm by 420 mm)
//       68  | A3 extra transverse paper (322 mm by 445 mm)
//       69  | Japanese Double Postcard (200 mm x 148 mm)
//       70  | A6 (105 mm x 148 mm)
//       71  | Japanese Envelope Kaku #2
//       72  | Japanese Envelope Kaku #3
//       73  | Japanese Envelope Chou #3
//       74  | Japanese Envelope Chou #4
//       75  | Letter Rotated (11in x 8 1/2 11 in)
//       76  | A3 Rotated (420 mm x 297 mm)
//       77  | A4 Rotated (297 mm x 210 mm)
//       78  | A5 Rotated (210 mm x 148 mm)
//       79  | B4 (JIS) Rotated (364 mm x 257 mm)
//       80  | B5 (JIS) Rotated (257 mm x 182 mm)
//       81  | Japanese Postcard Rotated (148 mm x 100 mm)
//       82  | Double Japanese Postcard Rotated (148 mm x 200 mm)
//       83  | A6 Rotated (148 mm x 105 mm)
//       84  | Japanese Envelope Kaku #2 Rotated
//       85  | Japanese Envelope Kaku #3 Rotated
//       86  | Japanese Envelope Chou #3 Rotated
//       87  | Japanese Envelope Chou #4 Rotated
//       88  | B6 (JIS) (128 mm x 182 mm)
//       89  | B6 (JIS) Rotated (182 mm x 128 mm)
//       90  | (12 in x 11 in)
//       91  | Japanese Envelope You #4
//       92  | Japanese Envelope You #4 Rotated
//       93  | PRC 16K (146 mm x 215 mm)
//       94  | PRC 32K (97 mm x 151 mm)
//       95  | PRC 32K(Big) (97 mm x 151 mm)
//       96  | PRC Envelope #1 (102 mm x 165 mm)
//       97  | PRC Envelope #2 (102 mm x 176 mm)
//       98  | PRC Envelope #3 (125 mm x 176 mm)
//       99  | PRC Envelope #4 (110 mm x 208 mm)
//       100 | PRC Envelope #5 (110 mm x 220 mm)
//       101 | PRC Envelope #6 (120 mm x 230 mm)
//       102 | PRC Envelope #7 (160 mm x 230 mm)
//       103 | PRC Envelope #8 (120 mm x 309 mm)
//       104 | PRC Envelope #9 (229 mm x 324 mm)
//       105 | PRC Envelope #10 (324 mm x 458 mm)
//       106 | PRC 16K Rotated
//       107 | PRC 32K Rotated
//       108 | PRC 32K(Big) Rotated
//       109 | PRC Envelope #1 Rotated (165 mm x 102 mm)
//       110 | PRC Envelope #2 Rotated (176 mm x 102 mm)
//       111 | PRC Envelope #3 Rotated (176 mm x 125 mm)
//       112 | PRC Envelope #4 Rotated (208 mm x 110 mm)
//       113 | PRC Envelope #5 Rotated (220 mm x 110 mm)
//       114 | PRC Envelope #6 Rotated (230 mm x 120 mm)
//       115 | PRC Envelope #7 Rotated (230 mm x 160 mm)
//       116 | PRC Envelope #8 Rotated (309 mm x 120 mm)
//       117 | PRC Envelope #9 Rotated (324 mm x 229 mm)
//       118 | PRC Envelope #10 Rotated (458 mm x 324 mm)
//
func (f *File) SetPageLayout(sheet string, opts ...PageLayoutOption) error {
	s, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	ps := s.PageSetUp
	if ps == nil {
		ps = new(xlsxPageSetUp)
		s.PageSetUp = ps
	}

	for _, opt := range opts {
		opt.setPageLayout(ps)
	}
	return err
}

// GetPageLayout provides a function to gets worksheet page layout.
//
// Available options:
//   PageLayoutOrientation(string)
//   PageLayoutPaperSize(int)
func (f *File) GetPageLayout(sheet string, opts ...PageLayoutOptionPtr) error {
	s, err := f.workSheetReader(sheet)
	if err != nil {
		return err
	}
	ps := s.PageSetUp

	for _, opt := range opts {
		opt.getPageLayout(ps)
	}
	return err
}

// workSheetRelsReader provides a function to get the pointer to the structure
// after deserialization of xl/worksheets/_rels/sheet%d.xml.rels.
func (f *File) workSheetRelsReader(path string) *xlsxWorkbookRels {
	if f.WorkSheetRels[path] == nil {
		_, ok := f.XLSX[path]
		if ok {
			c := xlsxWorkbookRels{}
			_ = xml.Unmarshal(namespaceStrictToTransitional(f.readXML(path)), &c)
			f.WorkSheetRels[path] = &c
		}
	}
	return f.WorkSheetRels[path]
}

// workSheetRelsWriter provides a function to save
// xl/worksheets/_rels/sheet%d.xml.rels after serialize structure.
func (f *File) workSheetRelsWriter() {
	for p, r := range f.WorkSheetRels {
		if r != nil {
			v, _ := xml.Marshal(r)
			f.saveFileList(p, v)
		}
	}
}

// fillSheetData ensures there are enough rows, and columns in the chosen
// row to accept data. Missing rows are backfilled and given their row number
func prepareSheetXML(xlsx *xlsxWorksheet, col int, row int) {
	rowCount := len(xlsx.SheetData.Row)
	if rowCount < row {
		// append missing rows
		for rowIdx := rowCount; rowIdx < row; rowIdx++ {
			xlsx.SheetData.Row = append(xlsx.SheetData.Row, xlsxRow{R: rowIdx + 1})
		}
	}
	rowData := &xlsx.SheetData.Row[row-1]
	fillColumns(rowData, col, row)
}

func fillColumns(rowData *xlsxRow, col, row int) {
	cellCount := len(rowData.C)
	if cellCount < col {
		for colIdx := cellCount; colIdx < col; colIdx++ {
			cellName, _ := CoordinatesToCellName(colIdx+1, row)
			rowData.C = append(rowData.C, xlsxC{R: cellName})
		}
	}
}

func makeContiguousColumns(xlsx *xlsxWorksheet, fromRow, toRow, colCount int) {
	for ; fromRow < toRow; fromRow++ {
		rowData := &xlsx.SheetData.Row[fromRow-1]
		fillColumns(rowData, colCount, fromRow)
	}
}
