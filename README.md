# filewatch

ローカルファイルを監視して、変更を別の場所へコピー（`sync`）、またはその場で正規表現置換（`replace`）する CLI。

## インストール

```sh
go build -o filewatch .
```

## 使い方

```
filewatch [-c 設定ファイル] sync
filewatch [-c 設定ファイル] replace {監視対象}
```

`-c` の既定値は `.filewatch.toml`。設定ファイルが無ければ空の設定で起動する。

### sync

監視元の変更を反映先へコピーする。起動時に初回コピーを行い、以降は変更・作成・削除を追従する。
対話プロンプトに `{監視元} {反映先}` を入力すると監視を追加できる。ファイル・ディレクトリどちらも可。

```
$ filewatch sync
>> ./src ./dist
added watch: /path/to/src -> /path/to/dist
設定を保存しますか? [y/N]: y
saved: .filewatch.toml
```

### replace

監視対象（ファイルまたはディレクトリ）内のファイルが書き換わるたびに、登録済みの正規表現置換をその場で適用する。
対話プロンプトに `{パターン} {置換後}` を入力するとパターンを追加できる。

```
$ filewatch replace ./docs
>> foo bar
added replace pattern
設定を保存しますか? [y/N]: y
```

パターンは Go の `regexp` 構文。置換後は `$1` などの後方参照が使える。
置換結果が元と同じ場合は書き戻さないので、自身の書き込みによる無限ループは起きない。

## 設定ファイル

```toml
[[sync]]
src = "./src"
dst = "./dist"

[[replace]]
pattern = "foo"
with = "bar"
```

`sync` / `replace` は対応するサブコマンドでのみ読まれる。

## 挙動メモ

- 書き込みは一時ファイル + `rename` のアトミック更新。
- 連続する Write イベントは 120ms まとめて 1 回に反映する。
- 反映先を監視元ディレクトリの内部に置くことはできない（コピーの無限ループ防止）。
- 終了は Ctrl+C。
