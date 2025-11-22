# ごろごろ将棋 (5x6)

Go 製の簡易エンジンとブラウザ UI で遊べる「ごろごろしょうぎ」のデモです。盤・駒は `main.go` のロジックと `web/` の静的ページで構成されます。

## 動かし方
- Go 1.25 以降を用意し、`go run .` を実行すると `http://localhost:8080` で UI が開きます。
- 駒をクリック（またはドラッグ）して移動・打ちができます。`最初からやり直す` ボタンで初期配置に戻ります。
- MCTS エンジンの学習結果はデフォルトで `data/` に保存され、`go run . -data-dir=/path/to/data` で保存先を変更できます。

## ベンチマーク
- TD エンジンが単位時間あたりに解析できる局面数は `go test -bench=BenchmarkTDUCBEngineStatesPerSecond ./game -run=^$` で測定できます。
- 上記ベンチマークで測定した最適化前後の `states/s` は以下の通りで、すべてのシナリオで高速化されています（Apple M2, Go 1.25）。

| Scenario | Config   | Before (states/s) | After (states/s) |
|----------|----------|-------------------|------------------|
| opening  | fast     | 65,663            | 107,395          |
| opening  | default  | 68,212            | 102,820          |
| tactical_scramble | fast | 47,500 | 72,982 |
| tactical_scramble | default | 51,044 | 80,380 |

## ルール
- 実装に基づくルールの詳細は `docs/RULES.md` を参照してください（反則チェックは二歩など未対応のものがあります）。

## TD エンジンのプロファイリング
- TD-UCB エンジンが有効なプレイヤーに対して `GET /api/engine/profile?player=top` を叩くと、直近で積算した主要処理の時間（ミリ秒）が JSON で得られます。
- `reset=1` をクエリに付けると、レスポンス返却後にカウンタをクリアできます。必要なシナリオでだけ値を集めたい場合に利用してください。
- `go test -bench=BenchmarkTDUCBEngineStatesPerSecond ./game -run=^$` を実行すると、states/s に加えて `td_next_ms/op`（NextMove 全体）、`td_sim_ms/op`（シミュレーション合計）など、内部処理ごとの平均ミリ秒もベンチ結果に含まれます。
