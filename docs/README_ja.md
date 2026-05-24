# Komari

![Badge](https://hitscounter.dev/api/hit?url=https%3A%2F%2Fgithub.com%2Fshaolonger%2Fkomari&label=&icon=github&color=%23a370f7&message=&style=flat&tz=UTC)

![komari](https://socialify.git.ci/shaolonger/komari/image?description=1&font=Inter&forks=1&issues=1&language=1&logo=https%3A%2F%2Fraw.githubusercontent.com%2Fkomari-monitor%2Fkomari-web%2Fd54ce1288df41ead08aa19f8700186e68028a889%2Fpublic%2Ffavicon.png&name=1&owner=1&pattern=Plus&pulls=1&stargazers=1&theme=Auto)

Komariは、サーバーのパフォーマンスを監視するためのシンプルで効率的なソリューションを提供することを目的とした、軽量の自己ホスト型サーバー監視ツールです。Webインターフェースを介してサーバーのステータスを表示し、軽量エージェントを介してデータを収集します。

[ドキュメント](https://komari-document.pages.dev/) | [Telegramグループ](https://t.me/komari_monitor)

## 特徴

- **軽量で効率的**: リソース消費が少なく、あらゆる規模のサーバーに適しています。
- **自己ホスト型**: データプライバシーを完全に制御でき、展開も簡単です。
- **Webインターフェース**: 直感的な監視ダッシュボードで、使いやすいです。

## クイックスタート

### 0. クラウドホスティングによるワンクリック展開

- Rainyun - CNY 4.5/月

[![](https://rainyun-apps.cn-nb1.rains3.com/materials/deploy-on-rainyun-cn.svg)](https://app.rainyun.com/apps/rca/store/6780/NzYxNzAz_)

- 1Panel アプリストア

1Panel アプリストアで利用可能です。「アプリストア」>「ユーティリティ」>「Komari」からインストールしてください。

### 1. ワンクリックインストールスクリプトを使用する

systemdを使用するディストリビューション（Ubuntu、Debianなど）に適しています。

```bash
curl -fsSL https://raw.githubusercontent.com/shaolonger/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

### 2. Docker展開

最大限のセキュリティ・コンプライアンスを確保するため、公式のDockerコンテナは**非rootユーザー**（`UID/GID 10001`）として実行されます。

#### オプションA：命名ボリューム（推奨）
Dockerは命名ボリュームの所有権を自動的に管理するため、非rootコンテナがデータを読み書きするのに最適な権限を確保できます。
```bash
# 1. 命名ボリュームを使用してコンテナを実行します
docker run -d \
  -p 25774:25774 \
  -v komari-data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest

# 2. 安全なローカル一時ファイルから初期管理者パスワードを取得します（初回ログイン成功後に自動で安全に削除されます）：
docker exec komari cat /app/data/init_password.txt
```

#### オプションB：ホストディレクトリのマウント
もしホストディレクトリをマウントする場合は、**必ず**事前にホスト側で適切な所有者権限を設定してください：
```bash
# 1. ディレクトリを作成し、非rootユーザーの所有権に設定します
mkdir -p ./data
chown -R 10001:10001 ./data

# 2. コンテナを実行します
docker run -d \
  -p 25774:25774 \
  -v $(pwd)/data:/app/data \
  --name komari \
  ghcr.io/shaolonger/komari:latest

# 3. 初期管理者パスワードを読み取ります：
cat ./data/init_password.txt
```

4. ブラウザで `http://<your_server_ip>:25774` にアクセスします。

> [!IMPORTANT]
> セキュリティ監査仕様に準拠するため、ランダムに生成されるデフォルトの管理者パスワードは、**永続化されるコンテナログやシステムサービスの起動ログには決して書き込まれません**。コンテナ内部の `/app/data/init_password.txt` ファイルに厳格かつ安全に書き込まれ、管理者が初めてログインに成功した直後に自動で安全に消去されます。起動前に環境変数 `ADMIN_USERNAME` と `ADMIN_PASSWORD` を使用して、独自の初期ユーザー名とパスワードを指定することも可能です。

### 3. バイナリファイル展開

1. Komariの[GitHubリリース](https://github.com/shaolonger/komari/releases)ページにアクセスして、お使いのオペレーティングシステム用の最新のバイナリをダウンロードします。
2. Komariを実行します:
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
3. ブラウザで `http://<your_server_ip>:25774` にアクセスします。デフォルトのポートは `25774` です。
4. デフォルトの管理者ユーザー名は `admin` です。ランダムに生成される初期パスワードは、実行ディレクトリ内の `data/init_password.txt` に安全に書き込まれ、コンソールやシステムログには記録されません。初回ログイン成功後にそのファイルが消去されていることを確認してください。または、起動前に環境変数 `ADMIN_USERNAME` と `ADMIN_PASSWORD` を設定して事前に指定することもできます。

> [!NOTE]
> バイナリに実行権限があることを確認してください（`chmod +x komari`）。データは実行ディレクトリの `data` フォルダに保存されます。

### 手動ビルド

#### 依存関係

- Go 1.18+ および Node.js 20+（手動ビルド用）

1. フロントエンドの静的ファイルをビルドします:
   ```bash
   git clone https://github.com/komari-monitor/komari-web
   cd komari-web
   npm install
   npm run build
   ```
2. バックエンドをビルドします:
   ```bash
   git clone https://github.com/shaolonger/komari
   cd komari
   ```
   ステップ1で生成された静的ファイルを `komari` プロジェクトのルートにある `/public/defaultTheme/dist` フォルダにコピーし、`komari-theme.json` と `preview.png`/`perview.png` を `/public/defaultTheme` にコピーします。
   ```bash
   go build -o komari
   ```
3. 実行:
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
   デフォルトのリスニングポートは `25774` です。`http://localhost:25774` にアクセスします。

## フロントエンド開発ガイド

[Komariテーマ開発ガイド | Komari](https://komari-document.pages.dev/dev/theme.html)

[CrowdinでKomariを翻訳する](https://crowdin.com/project/komari/invite?h=cd051bf172c9a9f7f1360e87ffb521692507706)

## クライアントエージェント開発ガイド

[Komariエージェント情報レポートおよびイベント処理ドキュメント](https://komari-document.pages.dev/dev/agent.html)

## 貢献

IssueやPull Requestを歓迎します！

## 謝辞

### 破碎工坊云 (Crash Work)

[破碎工坊云 - 効率的で安定した、安全な高防御サーバーとCDNソリューションを提供する専門的なクラウドコンピューティングプラットフォーム](https://www.crash.work/)

### DreamCloud

[DreamCloud - コストパフォーマンスに優れたアジア太平洋向け高防御直通](https://as211392.com/)

### 🚀 SharonNetworks スポンサー

[![Sharon Networks](https://raw.githubusercontent.com/komari-monitor/public/refs/heads/main/images/sharon-networks.webp)](https://sharon.io)

SharonNetworks は、あなたのビジネスの離陸を力強くサポートします！

アジア太平洋のデータセンターから中国最適化ネットワーク接続を提供。低レイテンシー & 高帯域幅、Tbps 級のローカル洗浄 DDoS 防御で、ビジネスとお客様の体験を守ります。コミュニティ [Telegram グループ](https://t.me/SharonNetwork) に参加すると、チャリティまたはグループ内抽選で無料利用のチャンスがあります。

### オープンソースコミュニティ

PR を送ってくれた方、テーマを作成してくれた全ての開発者

—— そして：こんなに暇でいられる自分に感謝

## Star履歴

[![Star History Chart](https://api.star-history.com/svg?repos=shaolonger/komari&type=Date)](https://www.star-history.com/#shaolonger/komari&Date)
