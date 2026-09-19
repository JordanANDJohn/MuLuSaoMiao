package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// excelEscape XML 转义（表内文本）
func excelEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// excelCell inlineStr 单元格（避免 sharedStrings，Excel/WPS/LibreOffice 均可打开）
func excelCell(ref, val string, bold bool) string {
	s := ""
	if bold {
		s = ` s="1"` // 表头加粗样式（对应 styles.xml 中 xf 1）
	}
	return fmt.Sprintf(`<c r="%s"%s t="inlineStr"><is><t>%s</t></is></c>`,
		ref, s, excelEscape(val))
}

// excelCol 列编号：0->A
func excelCol(idx int) string {
	s := ""
	idx++
	for idx > 0 {
		idx--
		s = string(rune('A'+idx%26)) + s
		idx /= 26
	}
	return s
}

// excelSheet XML 包装：cols + sheetData
func excelSheet(cols, sheetData string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<cols>%s</cols>
<sheetData>%s</sheetData>
</worksheet>`, cols, sheetData)
}

// excelRow 一行（bold=true 表头加粗）
func excelRow(row int, values []string, bold bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<row r="%d">`, row))
	for j, v := range values {
		b.WriteString(excelCell(fmt.Sprintf("%s%d", excelCol(j), row), v, bold))
	}
	b.WriteString(`</row>`)
	return b.String()
}

// SaveExcelReport 导出 .xlsx（OOXML，纯标准库 zip+xml 手写，零第三方依赖）
// Sheet1 风险汇总：6 类 + 说明 + 数量；Sheet2 明细：分类列 + 每行一条
func SaveExcelReport(rep ReportData, outPath string) error {
	// Sheet1 风险汇总
	cols1 := `<col min="1" max="1" width="14" customWidth="1"/><col min="2" max="2" width="24" customWidth="1"/><col min="3" max="3" width="70" customWidth="1"/><col min="4" max="4" width="10" customWidth="1"/>`
	var rows1 strings.Builder
	rows1.WriteString(excelRow(1, []string{"分类", "说明", "如何处置", "数量"}, true))
	r := 2
	rows1.WriteString(excelRow(r, []string{"目标", rep.Target, "", ""}, false))
	r++
	rows1.WriteString(excelRow(r, []string{"请求方法", rep.Method, "", ""}, false))
	r++
	rows1.WriteString(excelRow(r, []string{"开始时间", rep.Started.Format("2006-01-02 15:04:05"), "", ""}, false))
	r++
	rows1.WriteString(excelRow(r, []string{"生成路径", fmt.Sprint(rep.Total) + " 条", "", ""}, false))
	r++
	rows1.WriteString(excelRow(r, []string{"收录", fmt.Sprint(TotalKept(rep)) + " 条 / 剔除噪音 " + fmt.Sprint(rep.Dropped) + " 条", "", ""}, false))
	r++
	for _, c := range rep.Cats {
		rows1.WriteString(excelRow(r, []string{c.Key, c.Title, c.Desc, fmt.Sprint(len(c.Results))}, false))
		r++
	}
	rows1.WriteString(excelRow(r, []string{"说明", reportNotice, "", ""}, false))

	// Sheet2 明细（含 目标 列：多目标报告可按目标筛选）
	cols2 := `<col min="1" max="1" width="12" customWidth="1"/><col min="2" max="2" width="16" customWidth="1"/><col min="3" max="3" width="24" customWidth="1"/><col min="4" max="4" width="60" customWidth="1"/><col min="5" max="5" width="8" customWidth="1"/><col min="6" max="6" width="10" customWidth="1"/><col min="7" max="7" width="10" customWidth="1"/><col min="8" max="8" width="40" customWidth="1"/><col min="9" max="9" width="40" customWidth="1"/><col min="10" max="10" width="40" customWidth="1"/><col min="11" max="11" width="16" customWidth="1"/>`
	var rows2 strings.Builder
	rows2.WriteString(excelRow(1, []string{"分类", "分类说明", "目标", "URL", "状态", "大小", "耗时(ms)", "标题", "重定向", "最终URL", "Server"}, true))
	n := 0
	r = 2
	for _, c := range rep.Cats {
		for _, x := range c.Results {
			rows2.WriteString(excelRow(r, []string{
				c.Key, c.Title, x.Target, x.URL, fmt.Sprint(x.Status), humanSize(x.Size),
				fmt.Sprint(x.TimeMs), x.Title, redirectDesc(x), x.FinalURL, x.Server,
			}, false))
			r++
			n++
		}
	}
	rows2.WriteString(excelRow(r, []string{"说明", reportNotice, "", "", "", "", "", "", "", "", ""}, false))

	// Sheet3 全部 URL 汇总（便于复制；按分类列出）
	var rows3 strings.Builder
	rows3.WriteString(excelRow(1, []string{"分类", "URL"}, true))
	r = 2
	for _, c := range rep.Cats {
		for _, u := range c.URLs {
			rows3.WriteString(excelRow(r, []string{c.Key, u}, false))
			r++
		}
	}
	rows3.WriteString(excelRow(r, []string{"说明", reportNotice}, false))

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/worksheets/sheet2.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/worksheets/sheet3.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

	rootRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>
<sheet name="风险汇总" sheetId="1" r:id="rId1"/>
<sheet name="明细" sheetId="2" r:id="rId2"/>
<sheet name="URL汇总" sheetId="3" r:id="rId3"/>
</sheets>
</workbook>`

	workbookRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/>
<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

	sheet1 := excelSheet(cols1, rows1.String())
	sheet2 := excelSheet(cols2, rows2.String())
	sheet3 := excelSheet(`<col min="1" max="1" width="14" customWidth="1"/><col min="2" max="2" width="60" customWidth="1"/>`, rows3.String())

	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><sz val="11"/><name val="Calibri"/></font>
<font><b/><sz val="11"/><name val="Calibri"/></font>
</fonts>
<fills count="1"><fill><patternFill patternType="none"/></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="2">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>
</cellXfs>
</styleSheet>`

	var b bytes.Buffer
	zw := zip.NewWriter(&b)

	add := func(name, content string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write([]byte(content))
		return err
	}

	if err := add("[Content_Types].xml", contentTypes); err != nil {
		return err
	}
	if err := add("_rels/.rels", rootRels); err != nil {
		return err
	}
	if err := add("xl/workbook.xml", workbook); err != nil {
		return err
	}
	if err := add("xl/_rels/workbook.xml.rels", workbookRels); err != nil {
		return err
	}
	if err := add("xl/worksheets/sheet1.xml", sheet1); err != nil {
		return err
	}
	if err := add("xl/worksheets/sheet2.xml", sheet2); err != nil {
		return err
	}
	if err := add("xl/worksheets/sheet3.xml", sheet3); err != nil {
		return err
	}
	if err := add("xl/styles.xml", styles); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	if err := os.WriteFile(outPath, b.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "结果已保存: %s（%d 条，xlsx）\n", outPath, TotalKept(rep))
	return nil
}

// redirectDesc 展示重定向信息（与终端一致：链或 Location）
func redirectDesc(r Result) string {
	if len(r.Chain) > 0 {
		return "chain: " + strings.Join(r.Chain, " -> ")
	}
	return r.Location
}