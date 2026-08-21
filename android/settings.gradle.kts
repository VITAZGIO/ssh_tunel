// Корень сборки приложения. Рядом лежит core/ — модуль на Go, из которого
// собирается библиотека core.aar; Gradle её только подключает.
pluginManagement {
	repositories {
		google()
		mavenCentral()
		gradlePluginPortal()
	}
}
dependencyResolutionManagement {
	repositories {
		google()
		mavenCentral()
	}
}

rootProject.name = "ssh_tunnel_android"
include(":app")
