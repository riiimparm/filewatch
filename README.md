# filewatch

ローカルファイルを監視して、変更を別の場所へコピー（`sync`）、またはその場で正規表現置換（`replace`）する CLI。

## 利用シーン

**メモから静的サイトへ記事を流し込む。** Obsidian の vault で書いた記事と画像をブログのリポジトリへ `sync` し、vault 内の相対パス（`assets/...`）を公開 URL（`/images/...`）へ `replace` で書き換える。元のメモは書き換えずに済む。

```
sync    ~/memo/post.md     ~/blog/src/content/blog/post.md
sync    ~/memo/assets/post ~/blog/public/images/blog/post
replace ~/blog/src/content/blog/post.md 's|assets/post|/images/blog/post|g'
```

**生成物を配信先へ流す。** ビルド出力のディレクトリを `sync` し、配信先で消されたファイルは監視元から戻す。

**雛形を展開する。** テンプレートを配置先へ `sync` し、環境ごとの値を `replace` で埋める。

## インストール

```sh
go build -o filewatch .
```

## 使い方

```
filewatch [-c 設定ファイル] [-allow-missing] [sync|replace]
```

サブコマンドを省略すると設定ファイルの `sync` と `replace` を両方起動する。対話入力も種別を前置する形になる。

- `-c` の既定値は `.filewatch`。無ければ空の設定で起動する。
- `-allow-missing` を付けると、存在しない監視元も登録し、作られた時点で反映を始める（既定はそのエントリを飛ばしてエラー表示）。未作成の監視元はファイルとして扱う。
- 終了は Ctrl+C。

### sync

監視元の変更を反映先へコピーする。起動時に初回コピーを行い、以降は変更・作成・削除を追従する。
反映先も監視し、反映先だけが消された場合は監視元から復元する（監視元も消えていれば削除の反映とみなす）。

```
$ filewatch sync
>> ./src ./dist
added watch: /path/to/src -> /path/to/dist
設定を保存しますか? [y/N]: y
saved: .filewatch
```

監視元と反映先を入れ子にすることはできない。反映先が別の `sync` の監視元と重なるループも、起動時に設定全体を検査して経路を表示し終了する（終了コード1）。

```
$ filewatch
error: .filewatch: 監視対象がループします: /path/c -> /path/a -> /path/b -> /path/c
```

### replace

監視対象（ファイルまたはディレクトリ）が書き換わるたびに、sed の `s` コマンド形式 `s/{パターン}/{置換}/{フラグ}` をその場で適用する。

```
$ filewatch replace
>> ./docs s/foo/bar/g
added replace: /path/to/docs s/foo/bar/g
```

- 区切り文字は任意（`s|a/b|x|g`）。フラグは `g`（行内すべて。無ければ各行の最初の1件）と `i`（大文字小文字を無視）。
- パターンは Go の `regexp` 構文。置換側で `\1` と `&` が使える。sed と同じく行単位。
- `|` などシェルの特殊文字を含む式は `'...'` で囲む。
- 適用されるのは起動後の書き込みなので、既存の内容は保存し直すと置換される。
- 結果が元と同じなら書き戻さない。ただし `s/a/aa/g` のような収束しない式は検出しないので、書き換えが続く。

### sync と replace の組み合わせ

replace の監視対象が sync の反映先と重なる場合、コピー時にバッファ上で置換してから書き出す。二重書き込みにならず、未置換の中間状態もできない。監視元は書き換えない。
反映先が未作成でもルールは登録されるので、初回コピーから置換が効く。

### graph / unwatch

プロンプトで `graph` と入力すると、監視対象のつながりを木で表示する。

```
>> graph
/path/to/a
  [replace] s/x/X/
  -> /path/to/b
    [replace] s|assets/|/images/|g
```

`unwatch {監視元 または 監視対象}` でその監視を止める。解除後は設定ファイルからも消すか聞かれる。

## 設定ファイル

対話入力に種別を前置しただけの行形式。`#` 始まりと空行は無視。空白を含むパスや式は `'...'` / `"..."` で囲む。

```
# sync    {監視元}   {反映先}
# replace {監視対象} {sed置換}
sync ./src ./dist
replace ./docs s/foo/bar/g
```

## 挙動メモ

- 書き込みは一時ファイル + `rename` のアトミック更新。一時ファイルは書き込み先と同じディレクトリに作る。
- 4MB を超えるファイルは全体をメモリに載せず、一時ファイルへ1行ずつ流しながら置換する。
- 連続する Write イベントは 120ms まとめて 1 回に反映する。
