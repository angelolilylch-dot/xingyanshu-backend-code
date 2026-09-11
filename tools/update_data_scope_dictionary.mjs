import fs from "node:fs/promises";
import { FileBlob, SpreadsheetFile } from "@oai/artifact-tool";

const sourcePath = "M:/工作目录/星恒云创/功能结构图/v0.4/数据库字段字典.xlsx";
const outputPath = "M:/工作目录/星恒云创/功能结构图/v0.4/数据库字段字典.updated.xlsx";

const input = await FileBlob.load(sourcePath);
const workbook = await SpreadsheetFile.importXlsx(input);
const sheet = workbook.worksheets.getItem("字段字典");
const usedRange = sheet.getUsedRange();
const values = usedRange.values;
const duplicate = values.some((row) => row[1] === "sys_user" && row[3] === "data_scope");

if (!duplicate) {
  const rowNumber = values.length + 1;
  const newRow = sheet.getRange(`A${rowNumber}:L${rowNumber}`);
  const previousRow = sheet.getRange(`A${rowNumber - 1}:L${rowNumber - 1}`);
  newRow.copyFrom(previousRow, "all");
  newRow.values = [[
    "组织权限",
    "sys_user",
    "后台用户",
    "data_scope",
    "数据范围",
    "VARCHAR(20)",
    "否",
    "SELF",
    null,
    "数据范围（ALL 全平台 / CITY 本城市 / ORG 本组织 / SELF 仅本人）",
    "A03 / A01",
    "一期每个后台账号仅一个角色；页面展示中文名称，接口保存固定编码",
  ]];
}

await workbook.recalculate();
const check = await workbook.inspect({
  kind: "match",
  searchTerm: "data_scope",
  options: { maxResults: 10 },
  maxChars: 2000,
});
console.log(check.ndjson);

const output = await SpreadsheetFile.exportXlsx(workbook);
await fs.rm(outputPath, { force: true });
await output.save(outputPath);
