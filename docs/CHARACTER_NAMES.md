# Character Name Dataset

`web/src/data/novelCharacters.ts` 包含恰好 3,544 个唯一的人物显示名，全部取自金庸小说、《金瓶梅》以及四大名著（《红楼梦》《西游记》《水浒传》《三国演义》）。它是 build-time 固化数据：浏览器运行期间不会访问 Wikidata、CDN 或第三方 API。

## Scope

收录范围固定为以下作品：

- 金庸十五部武侠小说：《飞狐外传》《雪山飞狐》《连城诀》《天龙八部》《射雕英雄传》《白马啸西风》《鹿鼎记》《笑傲江湖》《书剑恩仇录》《神雕侠侣》《侠客行》《倚天屠龙记》《碧血剑》《鸳鸯刀》《越女剑》。
- 《金瓶梅》。
- 四大名著：《红楼梦》《西游记》《水浒传》《三国演义》。

不包含其它文学作品、历史人物条目或虚构作品中的非人物实体。

## Source and license

来源为 [Wikidata Query Service](https://query.wikidata.org/) 的结构化数据。Wikidata 主命名空间结构化数据采用 [CC0](https://www.wikidata.org/wiki/Wikidata:Licensing)。查询日期为 2026-09-11。

四大名著与《金瓶梅》按作品 QID 取角色（P674「角色」或反向 P1441「出现于作品」）；金庸作品不枚举 QID，而是通过作者（P50 = 金庸）关联后取角色，避免漏掉个别作品。核心查询形如：

```sparql
SELECT DISTINCT ?character ?characterLabel WHERE {
  {
    VALUES ?work { wd:Q8265 wd:Q70784 wd:Q70827 wd:Q70806 wd:Q753535 }
    { ?work wdt:P674 ?character } UNION { ?character wdt:P1441 ?work }
  } UNION {
    ?work wdt:P50 ?author . ?author rdfs:label "金庸"@zh .
    { ?work wdt:P674 ?character } UNION { ?character wdt:P1441 ?work }
  }
  ?character rdfs:label ?characterLabel . FILTER(LANG(?characterLabel) = "zh")
  FILTER( EXISTS { ?character wdt:P31/wdt:P279* wd:Q95074 }
       || EXISTS { ?character wdt:P31/wdt:P279* wd:Q5 } )
  FILTER NOT EXISTS { ?character wdt:P31/wdt:P279* wd:Q7725634 }
}
```

其中 Q95074 是 fictional character，Q5 是 human（《三国演义》等作品的曹操、刘备等历史人物属于此类），Q7725634 是 literary work（用于排除作品自身）。

### Curated supplement

Wikidata 对金庸部分作品（如《连城诀》《侠客行》《碧血剑》《书剑恩仇录》《白马啸西风》《越女剑》《鸳鸯刀》）缺少 P674/P1441 角色声明，会整部作品缺失。这些作品的主角与重要配角（如胡斐、袁承志、狄云、石破天、李文秀、阿青等）由人工校订补充，并附带补齐《金瓶梅》与四大名著中少量缺失的常用名（如陈经济、巧姐、唐僧）。补充名单中的群体名（如「江南七怪」）与非人物实体已排除。

## Processing

1. 下载结果执行 NFC normalization、trim、2–24 Unicode code-point 限制，删除 control/format/line-separator 字符、无 zh label 的 QID 和重复名称。
2. 用 `zhconv` 统一转换为简体中文（`zh-cn`），转换后再去重；「乾/昇/濛/荳/麹/璝/馀」等简繁同用或异体字保持原形。
3. 合并人工补充名单后去重，保留全部合格唯一名称（不做截断）。最终 3,544 项按 `SHA-256(Wikidata URI + NUL + label)` 确定性排序，便于复现。

## Runtime

运行时用 rejection sampling 从 32-bit CSPRNG 值映射到 3,544 项，避免简单 modulo 产生可测偏差。显示名不是安全身份：重复名称允许存在，真正的连接身份仍是随机 `ephemeralId`。用户改名只存在 React state 和当前 Relay Room RAM。
