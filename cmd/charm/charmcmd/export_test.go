// Copyright 2014-2016 Canonical Ltd.
// Licensed under the GPLv3, see LICENCE file for details.

package charmcmd

var (
	ClientGetArchive                       = &clientGetArchive
	CSClientServerURL                      = &csclientServerURL
	PluginTopicText                        = pluginTopicText
	ServerURL                              = serverURL
	TranslateError                         = translateError
	USSOTokenPath                          = ussoTokenPath
	PluginDescriptionLastCallReturnedCache = &pluginDescriptionLastCallReturnedCache
)

func ResetPluginDescriptionsResults() {
	pluginDescriptionsResults = nil
}
