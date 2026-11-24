package core

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

type MiddleAccelerationExcelRequest struct {
	RelayURL      string `json:"relayUrl"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	EntryID       string `json:"entryId"`
	MiddleDomain  string `json:"middleDomain"`
	FileName      string `json:"fileName"`
	FileBase64    string `json:"fileBase64"`
	MaxRetry      int    `json:"maxRetry"`
	Concurrency   int    `json:"concurrency"`
	ReuseExisting bool   `json:"reuseExisting"`
}

type MiddleAccelerationExcelResult struct {
	Success bool       `json:"success"`
	Message string     `json:"message"`
	Logs    []string   `json:"logs"`
	Output  string     `json:"output"`
	File    ExportFile `json:"file"`
}

type MiddleAccelerationExcelBytesResult struct {
	Success bool
	Message string
	Logs    []string
	Output  string
	File    ExportBytes
}

type MiddleAccelerationExcelSavedResult struct {
	Success  bool     `json:"success"`
	Message  string   `json:"message"`
	Logs     []string `json:"logs"`
	Output   string   `json:"output"`
	FilePath string   `json:"filePath"`
	FileName string   `json:"fileName"`
}

func (s *Service) RunMiddleAccelerationExcel(req MiddleAccelerationExcelRequest, emit func(string)) (MiddleAccelerationExcelResult, error) {
	fileResult, err := s.RunMiddleAccelerationExcelBytes(req, emit)
	if err != nil {
		return MiddleAccelerationExcelResult{Logs: fileResult.Logs}, err
	}
	return MiddleAccelerationExcelResult{
		Success: fileResult.Success,
		Message: fileResult.Message,
		Logs:    fileResult.Logs,
		Output:  fileResult.Output,
		File: ExportFile{
			FileName: fileResult.File.FileName,
			MimeType: fileResult.File.MimeType,
			Base64:   base64.StdEncoding.EncodeToString(fileResult.File.Data),
		},
	}, nil
}

func (s *Service) RunMiddleAccelerationExcelBytes(req MiddleAccelerationExcelRequest, emit func(string)) (MiddleAccelerationExcelBytesResult, error) {
	result := MiddleAccelerationExcelBytesResult{Logs: []string{}}
	logf := func(line string) {
		result.Logs = append(result.Logs, line)
		if emit != nil {
			emit(line)
		}
	}
	if strings.TrimSpace(req.FileBase64) == "" {
		return result, errors.New("请先上传 Excel 文件")
	}
	data, err := base64.StdEncoding.DecodeString(req.FileBase64)
	if err != nil {
		return result, errors.New("Excel 文件解码失败")
	}
	rows, err := readXLSXRows(data)
	if err != nil {
		return result, err
	}
	if len(rows) < 2 {
		return result, errors.New("Excel 没有可处理的数据")
	}
	linkCol, err := findNodeLinkColumn(rows, logf)
	if err != nil {
		return result, err
	}
	links := make([]string, 0, len(rows)-1)
	rowIndexes := make([]int, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		link := cellAt(rows[i], linkCol)
		if strings.TrimSpace(link) == "" {
			continue
		}
		links = append(links, link)
		rowIndexes = append(rowIndexes, i)
	}
	if len(links) == 0 {
		return result, errors.New("Excel 的节点链接列为空")
	}
	logf(fmt.Sprintf("Excel 读取完成：%d 条节点链接", len(links)))

	middle, err := s.RunMiddleAccelerationWithLogger(MiddleAccelerationRequest{
		RelayURL:      req.RelayURL,
		Username:      req.Username,
		Password:      req.Password,
		EntryID:       req.EntryID,
		MiddleDomain:  req.MiddleDomain,
		InputText:     strings.Join(links, "\n"),
		MaxRetry:      req.MaxRetry,
		Concurrency:   req.Concurrency,
		ReuseExisting: req.ReuseExisting,
	}, emit)
	if err != nil {
		return result, err
	}
	result.Logs = append(result.Logs, middle.Logs...)
	result.Output = middle.Output
	if len(middle.Items) != len(links) {
		return result, errors.New("中转结果数量与 Excel 链接数量不一致")
	}

	middleByRow := map[int]string{}
	for i, rowIndex := range rowIndexes {
		if middle.Items[i].Success {
			middleByRow[rowIndex] = middle.Items[i].Output
		}
	}
	outputRows, images := buildMiddleExcelRows(rows, linkCol, middleByRow)
	fileData, err := buildSimpleXLSX(outputRows, images)
	if err != nil {
		return result, err
	}
	name := strings.TrimSuffix(req.FileName, filepath.Ext(req.FileName))
	if name == "" {
		name = "中转结果"
	}
	result.File = ExportBytes{
		FileName: safeFileName("中转-" + name + ".xlsx"),
		MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Data:     fileData,
	}
	successCount := 0
	for _, item := range middle.Items {
		if item.Success {
			successCount++
		}
	}
	result.Success = successCount == len(links)
	result.Message = fmt.Sprintf("Excel 中转完成：成功 %d 条，失败 %d 条", successCount, len(links)-successCount)
	logf(result.Message)
	return result, nil
}

func readXLSXRows(data []byte) ([][]string, error) {
	if int64(len(data)) > 32<<20 {
		return nil, errors.New("Excel 文件超过 32MB 大小限制")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("Excel 文件格式无效")
	}
	shared := []string{}
	if text, ok := zipText(zr, "xl/sharedStrings.xml"); ok {
		shared = parseSharedStrings(text)
	}
	sheet, ok := zipText(zr, "xl/worksheets/sheet1.xml")
	if !ok {
		return nil, errors.New("Excel 未找到第一个工作表")
	}
	rows, err := parseSheetRows(sheet, shared)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

const (
	maxExcelEntryBytes = 16 << 20
	maxExcelRows       = 20000
	maxExcelColumns    = 256
)

func zipText(zr *zip.Reader, name string) (string, bool) {
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", false
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, maxExcelEntryBytes+1))
		_ = rc.Close()
		if readErr != nil || int64(len(data)) > maxExcelEntryBytes {
			return "", false
		}
		return string(data), true
	}
	return "", false
}

func parseSharedStrings(text string) []string {
	type textNode struct {
		Text string `xml:",chardata"`
	}
	type richText struct {
		Text textNode `xml:"t"`
	}
	type sharedItem struct {
		Text string     `xml:"t"`
		Runs []richText `xml:"r"`
	}
	type sharedStrings struct {
		Items []sharedItem `xml:"si"`
	}
	var doc sharedStrings
	if err := xml.Unmarshal([]byte(text), &doc); err != nil {
		return nil
	}
	values := make([]string, 0, len(doc.Items))
	for _, item := range doc.Items {
		if item.Text != "" {
			values = append(values, item.Text)
			continue
		}
		var parts []string
		for _, run := range item.Runs {
			parts = append(parts, run.Text.Text)
		}
		values = append(values, strings.Join(parts, ""))
	}
	return values
}

func parseSheetRows(text string, shared []string) ([][]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	rows := [][]string{}
	currentRow := -1
	for {
		token, err := decoder.Token()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "c" {
			continue
		}
		ref := attrValue(start.Attr, "r")
		cellType := attrValue(start.Attr, "t")
		col, row := cellRef(ref)
		if row <= 0 {
			if currentRow < 0 {
				row = 1
			} else {
				row = currentRow + 1
			}
		}
		if row > maxExcelRows {
			return nil, fmt.Errorf("Excel 行数超过 %d 行上限", maxExcelRows)
		}
		for len(rows) < row {
			rows = append(rows, []string{})
		}
		if row-1 > currentRow {
			currentRow = row - 1
		}
		if col < 0 {
			col = len(rows[row-1])
		}
		if col >= maxExcelColumns {
			return nil, fmt.Errorf("Excel 列数超过 %d 列上限", maxExcelColumns)
		}
		value := readCellValue(decoder, cellType, shared)
		for len(rows[row-1]) <= col {
			rows[row-1] = append(rows[row-1], "")
		}
		rows[row-1][col] = value
	}
	return rows, nil
}

func readCellValue(decoder *xml.Decoder, cellType string, shared []string) string {
	var value string
	for {
		token, err := decoder.Token()
		if err != nil {
			return value
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "v" || t.Name.Local == "t" {
				var text string
				_ = decoder.DecodeElement(&text, &t)
				value = text
			}
		case xml.EndElement:
			if t.Name.Local == "c" {
				if cellType == "s" {
					index, _ := strconv.Atoi(value)
					if index >= 0 && index < len(shared) {
						return shared[index]
					}
				}
				return html.UnescapeString(value)
			}
		}
	}
}

func attrValue(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func cellRef(ref string) (int, int) {
	col := 0
	rowText := ""
	for _, r := range ref {
		if r >= 'A' && r <= 'Z' {
			col = col*26 + int(r-'A'+1)
			continue
		}
		if r >= 'a' && r <= 'z' {
			col = col*26 + int(r-'a'+1)
			continue
		}
		if r >= '0' && r <= '9' {
			rowText += string(r)
		}
	}
	row, _ := strconv.Atoi(rowText)
	return col - 1, row
}

func findNodeLinkColumn(rows [][]string, logf func(string)) (int, error) {
	if len(rows) == 0 {
		return -1, errors.New("Excel 内容为空")
	}
	header := rows[0]
	// 1. 尝试匹配表头关键字
	keywords := []string{
		"网络链接", "节点链接", "协议链接", "节点", "链接", "代理链接", "代理",
		"url", "link", "proxy", "node", "address", "节点地址", "网络地址",
	}
	for _, kw := range keywords {
		for col, val := range header {
			cleanVal := strings.ToLower(strings.TrimSpace(val))
			if cleanVal == kw || strings.Contains(cleanVal, kw) {
				if logf != nil {
					logf(fmt.Sprintf("识别到节点链接列：第 %d 列（表头：%q）", col+1, strings.TrimSpace(val)))
				}
				return col, nil
			}
		}
	}

	// 2. 表头未命中，基于前若干行数据内容嗅探
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	checkRows := len(rows)
	if checkRows > 20 {
		checkRows = 20
	}
	bestCol := -1
	bestScore := 0
	for col := 0; col < maxCols; col++ {
		score := 0
		for r := 1; r < checkRows; r++ {
			val := strings.TrimSpace(cellAt(rows[r], col))
			if val == "" {
				continue
			}
			if strings.Contains(val, "://") {
				score += 2
			} else if _, err := parseMiddleNodeLink(val); err == nil {
				score += 2
			}
		}
		if score > bestScore {
			bestScore = score
			bestCol = col
		}
	}
	if bestCol >= 0 && bestScore >= 1 {
		colName := cellAt(header, bestCol)
		if logf != nil {
			logf(fmt.Sprintf("内容嗅探识别节点链接列：第 %d 列（表头：%q）", bestCol+1, colName))
		}
		return bestCol, nil
	}

	return -1, errors.New("Excel 未找到节点链接列，请确认包含“网络链接”、“节点链接”、“URL”等表头或包含有效协议链接")
}

func findColumn(header []string, name string) int {
	for i, value := range header {
		if strings.TrimSpace(value) == name {
			return i
		}
	}
	return -1
}

func cellAt(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return row[col]
}

func buildMiddleExcelRows(inputRows [][]string, linkCol int, middleByRow map[int]string) ([][]string, []xlsxImage) {
	header := append([]string{}, inputRows[0]...)
	qrCol := findColumn(header, "二维码")
	insertedHeader := insertStringAt(header, linkCol+1, "中转链接")
	if qrCol >= 0 && qrCol >= linkCol+1 {
		qrCol++
	}
	middleQRCol := qrCol + 1
	if qrCol >= 0 {
		insertedHeader = insertStringAt(insertedHeader, middleQRCol, "中转二维码")
	} else {
		middleQRCol = len(insertedHeader)
		insertedHeader = append(insertedHeader, "中转二维码")
	}

	outputRows := [][]string{insertedHeader}
	images := []xlsxImage{}
	for i := 1; i < len(inputRows); i++ {
		row := append([]string{}, inputRows[i]...)
		link := cellAt(row, linkCol)
		middleLink := middleByRow[i]
		row = insertStringAt(row, linkCol+1, middleLink)
		if qrCol >= 0 {
			row = insertStringAt(row, middleQRCol, "扫码")
		} else {
			row = append(row, "扫码")
		}
		for len(row) < len(insertedHeader) {
			row = append(row, "")
		}
		if qrCol >= 0 {
			row[qrCol] = "扫码"
		}
		if qrCol >= 0 && link != "" {
			images = append(images, xlsxImage{Row: len(outputRows), Col: qrCol, Name: fmt.Sprintf("qr%d.png", len(images)+1), Text: link})
		}
		if middleLink != "" {
			images = append(images, xlsxImage{Row: len(outputRows), Col: middleQRCol, Name: fmt.Sprintf("qr%d.png", len(images)+1), Text: middleLink})
		}
		outputRows = append(outputRows, row)
	}
	return outputRows, images
}

func insertStringAt(values []string, index int, value string) []string {
	if index < 0 || index >= len(values) {
		return append(values, value)
	}
	values = append(values, "")
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}
