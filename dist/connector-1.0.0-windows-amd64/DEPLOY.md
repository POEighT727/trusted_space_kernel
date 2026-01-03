# 杩炴帴鍣ㄩ儴缃茶鏄?

## 绯荤粺瑕佹眰

- Windows x64
- 缃戠粶杩炴帴鍒板唴鏍告湇鍔″櫒
- 闃茬伀澧欏厑璁歌繛鎺ュ埌鍐呮牳鏈嶅姟鍣ㄧ鍙ｏ紙榛樿 50051锛?

## 閮ㄧ讲姝ラ

### 姝ラ 1: 瑙ｅ帇鍙戝竷鍖?

浣跨敤瑙ｅ帇宸ュ叿瑙ｅ帇 connector-{version}-windows-amd64.zip

### 姝ラ 2: 閰嶇疆杩炴帴鍣?

1. 澶嶅埗閰嶇疆妯℃澘锛?
   `powershell
   Copy-Item config\connector-template.yaml config\connector.yaml
   `

2. 缂栬緫 config\connector.yaml锛岃缃細
   - 杩炴帴鍣↖D
   - 瀹炰綋绫诲瀷
   - 鍏挜
   - 鍐呮牳鏈嶅姟鍣ㄥ湴鍧€

### 姝ラ 3: 棣栨杩愯骞舵敞鍐?

`powershell
.\connector.exe -config config\connector.yaml
`

棣栨杩愯浼氳嚜鍔細
- 杩炴帴鍒板唴鏍告湇鍔″櫒
- 娉ㄥ唽杩炴帴鍣?
- 鑾峰彇骞朵繚瀛樿瘉涔?

### 姝ラ 4: 楠岃瘉杩炴帴

杩炴帴鎴愬姛鍚庯紝浣犱細鐪嬪埌锛?
`
鉁?杩炴帴鎴愬姛锛佽繛鎺ュ櫒ID: your-connector-id
`

### 姝ラ 5: 浣跨敤杩炴帴鍣?

杩炴帴鍣ㄥ惎鍔ㄥ悗锛屼綘鍙互浣跨敤浜や簰寮忓懡浠わ細
- 鍒涘缓棰戦亾
- 鍙戦€?鎺ユ敹鏁版嵁
- 鍙戦€?鎺ユ敹鏂囦欢
- 鏌ョ湅棰戦亾淇℃伅
- 绛夌瓑

## 瀹夊叏娉ㄦ剰浜嬮」

1. **璇佷功瀹夊叏**锛?
   - 璇佷功鏂囦欢鍖呭惈鏁忔劅淇℃伅锛岃濡ュ杽淇濈
   - 涓嶈灏嗚瘉涔︽枃浠舵彁浜ゅ埌鐗堟湰鎺у埗绯荤粺

2. **缃戠粶瀹夊叏**锛?
   - 浣跨敤VPN鎴栦笓鐢ㄧ綉缁滆繛鎺?
   - 閰嶇疆闃茬伀澧欒鍒欙紝闄愬埗璁块棶鏉ユ簮

3. **閰嶇疆瀹夊叏**锛?
   - 涓嶈鍦ㄧ敓浜х幆澧冧腑浣跨敤榛樿閰嶇疆
   - 瀹氭湡鏇存柊杩炴帴鍣ㄧ増鏈?

## 鏇存柊杩炴帴鍣?

1. 澶囦唤褰撳墠閰嶇疆鍜岃瘉涔︼細
   `powershell
   Copy-Item -Recurse config backup\config
   Copy-Item -Recurse certs backup\certs
   `

2. 瑙ｅ帇鏂扮増鏈繛鎺ュ櫒

3. 鎭㈠閰嶇疆鍜岃瘉涔︼細
   `powershell
   Copy-Item backup\config\* config\
   Copy-Item backup\certs\* certs\
   `

4. 杩愯鏂扮増鏈繛鎺ュ櫒

## 鍗歌浇

鐩存帴鍒犻櫎杩炴帴鍣ㄧ洰褰曞嵆鍙€傝瘉涔﹀拰閰嶇疆鍙互淇濈暀浠ュ灏嗘潵浣跨敤銆?
