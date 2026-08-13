package errcode

var codeRegistry = []CodeInfo{
	{uint64(ErrConnNotFound), "CONN_NOT_FOUND"},
	{uint64(ErrConnClosed), "CONN_CLOSED"},
	{uint64(ErrSendFailed), "SEND_FAILED"},
	{uint64(ErrRecvTimeout), "RECV_TIMEOUT"},
	{uint64(ErrConnDropped), "CONN_DROPPED"},
	{uint64(ErrActionCanceled), "ACTION_CANCELED"},
	{uint64(ErrEncodeFailed), "ENCODE_FAILED"},
	{uint64(ErrParseFailed), "PARSE_FAILED"},
	{uint64(ErrCreateMsg), "CREATE_MSG"},
	{uint64(ErrBindField), "BIND_FIELD"},
	{uint64(ErrSerialize), "SERIALIZE"},
	{uint64(ErrExecFailed), "EXEC_FAILED"},
	{uint64(ErrListenTimeout), "LISTEN_TIMEOUT"},
	{uint64(ErrListenRegister), "LISTEN_REGISTER"},
	{uint64(ErrAddrEmpty), "ADDR_EMPTY"},
	{uint64(ErrURLEmpty), "URL_EMPTY"},
	{uint64(ErrURLScheme), "URL_SCHEME"},
	{uint64(ErrUnknownPattern), "UNKNOWN_PATTERN"},
	{uint64(ErrHTTPBuild), "HTTP_BUILD"},
	{uint64(ErrHTTPReadBody), "HTTP_READ_BODY"},
	{uint64(ErrMarshalBody), "MARSHAL_BODY"},
	{uint64(ErrHTTPStatus), "HTTP_STATUS"},
	{uint64(ErrHeartbeatConfig), "HEARTBEAT_CONFIG"},
	{uint64(ErrStateConfig), "STATE_CONFIG"},
	{uint64(ErrLuaNotInit), "LUA_NOT_INIT"},
	{uint64(ErrLuaNoScript), "LUA_NO_SCRIPT"},
	{uint64(ErrLuaExecFailed), "LUA_EXEC_FAILED"},
	{uint64(ErrLuaScriptCheck), "LUA_SCRIPT_CHECK"},
	{uint64(ErrCallbackLua), "CALLBACK_LUA"},
	{uint64(ErrCallbackParse), "CALLBACK_PARSE"},
}

var codeNameIndex = func() map[uint64]string {
	index := make(map[uint64]string, len(codeRegistry))
	for _, code := range codeRegistry {
		index[code.Code] = code.Name
	}
	return index
}()

func (c ErrorCode) String() string { return codeNameIndex[uint64(c)] }

func AllCodes() []CodeInfo { return append([]CodeInfo(nil), codeRegistry...) }
