# 杩炴帴鍣ㄧ嫭绔嬮儴缃插寘

## 蹇€熷紑濮?

### 1. 閰嶇疆杩炴帴鍣?

缂栬緫 config\connector-template.yaml锛屼慨鏀逛互涓嬮厤缃細

`yaml
connector:
  id: "your-connector-id"        # 淇敼涓轰綘鐨勮繛鎺ュ櫒ID
  entity_type: "data_source"     # 淇敼涓轰綘鐨勫疄浣撶被鍨?
  public_key: "your-public-key" # 淇敼涓轰綘鐨勫叕閽?

kernel:
  address: "192.168.1.100"       # 淇敼涓哄唴鏍告湇鍔″櫒鍦板潃
  port: 50051

security:
  ca_cert_path: "certs\ca.crt"
  client_cert_path: "certs\connector-X.crt"
  client_key_path: "certs\connector-X.key"
  server_name: "trusted-data-space-kernel"
`

灏?config\connector-template.yaml 澶嶅埗涓?config\connector.yaml锛?

`powershell
Copy-Item config\connector-template.yaml config\connector.yaml
# 鐒跺悗缂栬緫 config\connector.yaml
`

### 2. 棣栨杩愯锛堣嚜鍔ㄦ敞鍐岋級

棣栨杩愯鏃朵細鑷姩杩炴帴鍒板唴鏍稿苟娉ㄥ唽鑾峰彇璇佷功锛?

`powershell
.\connector.exe -config config\connector.yaml
`

棣栨杩愯鎴愬姛鍚庯紝璇佷功浼氳嚜鍔ㄤ繚瀛樺埌 certs\ 鐩綍銆?

### 3. 鍚庣画杩愯

璇佷功鑾峰彇鍚庯紝鍚庣画杩愯鐩存帴浣跨敤宸蹭繚瀛樼殑璇佷功锛?

`powershell
.\connector.exe -config config\connector.yaml
`

## 鐩綍缁撴瀯

`
connector-{version}/
鈹溾攢鈹€ connector.exe      # 杩炴帴鍣ㄥ彲鎵ц鏂囦欢
鈹溾攢鈹€ config/
鈹?  鈹斺攢鈹€ connector-template.yaml  # 閰嶇疆妯℃澘
鈹溾攢鈹€ certs/             # 璇佷功鐩綍锛堥娆¤繍琛屽悗鑷姩鐢熸垚锛?
鈹溾攢鈹€ received/          # 鎺ユ敹鏂囦欢鐩綍
鈹斺攢鈹€ README.md          # 鏈枃浠?
`

## 鍛戒护璇存槑

杩炴帴鍣ㄦ敮鎸佷互涓嬪懡浠わ細

- create <channel-id> <data-topic> <receiver-id1,receiver-id2,...> - 鍒涘缓棰戦亾
- sendto <channel-id> <message> - 鍙戦€佹暟鎹埌棰戦亾
- sendto <channel-id> <file-path> - 鍙戦€佹枃浠跺埌棰戦亾
- 
eceive <channel-id> - 鎺ユ敹棰戦亾鏁版嵁
- subscribe <channel-id> - 璁㈤槄棰戦亾
- channels - 鏌ョ湅褰撳墠鍙備笌鐨勯閬?
- status - 鏌ョ湅杩炴帴鍣ㄧ姸鎬?
- status <active|inactive|closed> - 璁剧疆杩炴帴鍣ㄧ姸鎬?
- discover - 鍙戠幇鍏朵粬杩炴帴鍣?
- info <connector-id> - 鏌ョ湅杩炴帴鍣ㄤ俊鎭?
- exit 鎴?quit - 閫€鍑?

## 鏁呴殰鎺掓煡

### 杩炴帴澶辫触

1. 妫€鏌ュ唴鏍告湇鍔″櫒鍦板潃鍜岀鍙ｆ槸鍚︽纭?
2. 妫€鏌ョ綉缁滆繛閫氭€э細ping <kernel-address>
3. 妫€鏌ラ槻鐏鏄惁鍏佽绔彛 50051

### 璇佷功闂

1. 鍒犻櫎 certs\ 鐩綍涓嬬殑璇佷功鏂囦欢
2. 閲嶆柊杩愯杩炴帴鍣ㄨ繘琛岄娆℃敞鍐?

### 鏇村甯姪

璇峰弬鑰冨畬鏁撮儴缃叉枃妗ｏ細docs\DEPLOYMENT.md

