package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// docxEscape WordprocessingML 文本转义
func docxEscape(s string) string {
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

// docxRun 一段带样式的文本 run
func docxRun(text string, bold bool, sz int) string {
	rpr := ""
	if bold {
		rpr += `<w:b/>`
	}
	if sz > 0 {
		rpr += fmt.Sprintf(`<w:sz w:val="%d"/>`, sz) // 半磅
	}
	if rpr != "" {
		rpr = "<w:rPr>" + rpr + "</w:rPr>"
	}
	return fmt.Sprintf(`<w:r>%s<w:t xml:space="preserve">%s</w:t></w:r>`, rpr, docxEscape(text))
}

// docxPara 段落（可设置对齐） al: left/center/right
func docxPara(runs string, align string) string {
	pPr := ""
	if align != "" {
		pPr = fmt.Sprintf(`<w:pPr><w:jc w:val="%s"/></w:pPr>`, align)
	}
	return "<w:p>" + pPr + runs + "</w:p>"
}

// SaveDocxReport 导出 .docx（WordprocessingML，纯标准库 zip+xml 手写，零第三方依赖）
// 生成：标题 + 摘要 + 6 个风险分类小节（各含提示与表格）+ 末尾说明
func SaveDocxReport(rep ReportData, outPath string) error {
	headers := []string{"#", "URL", "状态", "大小", "耗时(ms)", "标题", "重定向", "Server"}
	widths := []int{4, 32, 6, 8, 8, 16, 16, 10} // 列宽百分比，合计 100

	// 标题与摘要
	var body strings.Builder
	body.WriteString(docxPara(docxRun("MuLuSaoMiao 扫描报告", true, 36), "center"))
	body.WriteString(docxPara(docxRun("目标: "+rep.Target, false, 0), ""))
	body.WriteString(docxPara(docxRun("请求方法: "+rep.Method, false, 0), ""))
	body.WriteString(docxPara(docxRun("开始时间: "+rep.Started.Format("2006-01-02 15:04:05"), false, 0), ""))
	body.WriteString(docxPara(docxRun(
		fmt.Sprintf("生成路径: %d 条 / 收录: %d 条 / 剔除噪音: %d 条", rep.Total, TotalKept(rep), rep.Dropped), false, 0), ""))

	shown := 0
	for ci, c := range rep.Cats {
		has := len(c.Results) > 0
		if has {
			shown++
		}
		// 分类小节标题 + 提示（空分类也展示，保证 6 部分完整）
		body.WriteString(docxPara(docxRun(
			fmt.Sprintf("%d. %s（%s）— %d 条", ci+1, c.Key, c.Title, len(c.Results)), true, 22), ""))
		body.WriteString(docxPara(docxRun("提示: "+c.Desc, false, 0), ""))
		if !has {
			body.WriteString(docxPara(docxRun("（无命中）", false, 0), ""))
			continue
		}

		// 表格：表头 + 数据
		var t strings.Builder
		t.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="5000" w:type="pct"/>` +
			`<w:tblLayout w:type="fixed"/>` +
			`<w:tblBorders>` +
			`<w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
			`<w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
			`<w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
			`<w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
			`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
			`<w:insideV w:val="single" w:sz="4" w:space="0" w:color="auto"/>` +
			`</w:tblBorders></w:tblPr>`)

		t.WriteString(`<w:tr>`)
		for i, h := range headers {
			t.WriteString(docxCell(h, true, widths[i]))
		}
		t.WriteString(`</w:tr>`)

		for i, r := range c.Results {
			t.WriteString(`<w:tr>`)
			vals := []string{
				fmt.Sprint(i + 1),
				r.URL,
				fmt.Sprint(r.Status),
				humanSize(r.Size),
				fmt.Sprint(r.TimeMs),
				r.Title,
				redirectDesc(r),
				r.Server,
			}
			for j, v := range vals {
				t.WriteString(docxCell(v, false, widths[j]))
			}
			t.WriteString(`</w:tr>`)
		}
		t.WriteString(`</w:tbl>`)
		body.WriteString(t.String())

		// 本分类 URL 列表（便于复制，每行一个 URL）
		body.WriteString(docxPara(docxRun(fmt.Sprintf("本类全部 URL（%d）：", len(c.URLs)), true, 18), ""))
		for _, u := range c.URLs {
			body.WriteString(docxPara(docxRun(u, false, 0), ""))
		}
		body.WriteString(docxPara(docxRun("", false, 0), ""))
	}

	if shown == 0 {
		body.WriteString(docxPara(docxRun("未发现 6 类有价值结果。", false, 0), ""))
	}
	// 全部 URL 汇总（置于说明之前）
	all := AllURLs(rep)
	if len(all) > 0 {
		body.WriteString(docxPara(docxRun(fmt.Sprintf("全部 URL 汇总（%d）：", len(all)), true, 22), ""))
		for _, u := range all {
			body.WriteString(docxPara(docxRun(u, false, 0), ""))
		}
		body.WriteString(docxPara(docxRun("", false, 0), ""))
	}
	// 末尾说明（第 7 部分）
	body.WriteString(docxPara(docxRun(reportNotice, false, 0), ""))

	// WordprocessingML 文档骨架
	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>` + body.String() + `
<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr>
</w:body>
</w:document>`

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`

	rootRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

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
	if err := add("word/document.xml", document); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	if err := os.WriteFile(outPath, b.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "结果已保存: %s（%d 条，docx）\n", outPath, TotalKept(rep))
	return nil
}

// docxCell 表格单元格（表头加粗+灰底；widthPct 为列宽百分比，pct 单位 = 百分比×50）
func docxCell(text string, bold bool, widthPct int) string {
	rpr := ""
	shd := ""
	if bold {
		rpr = `<w:b/>`
		shd = `<w:shd w:val="clear" w:color="auto" w:fill="D9D9D9"/>`
	}
	rprFull := ""
	if rpr != "" {
		rprFull = "<w:rPr>" + rpr + "</w:rPr>"
	}
	// 关键：<w:t> 必须包在 <w:r> 内，且 <w:rPr> 是 <w:r> 的子元素，否则 Word/WPS 忽略文本
	return fmt.Sprintf(`<w:tc><w:tcPr><w:tcW w:w="%d" w:type="pct"/>%s</w:tcPr><w:p><w:r>%s<w:t xml:space="preserve">%s</w:t></w:r></w:p></w:tc>`,
		widthPct*50, shd, rprFull, docxEscape(text))
}