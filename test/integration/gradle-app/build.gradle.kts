plugins {
    java
}

repositories {
    maven {
        val mirrorUrl = project.findProperty("mirrorUrl") as String?
            ?: "http://localhost:${project.findProperty("mirrorPort") ?: "8080"}"
        url = uri("$mirrorUrl/cached/tjenestespesifikasjoner")
        isAllowInsecureProtocol = true
    }
    mavenCentral()
}

dependencies {
    implementation("no.nav.tjenestespesifikasjoner:aktorid-jaxws:1.2019.12.18-12.22-ce897c4eb2c1")
}

tasks.register("resolveDeps") {
    doLast {
        configurations.runtimeClasspath.get().resolve()
        println("Dependencies resolved successfully")
    }
}
