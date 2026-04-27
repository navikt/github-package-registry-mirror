plugins {
    java
}

repositories {
    maven {
        url = uri("http://localhost:${project.findProperty("mirrorPort") ?: "8080"}/cached/tjenestespesifikasjoner")
        isAllowInsecureProtocol = true
    }
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
